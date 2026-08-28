package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	mathrand "math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"
)

const (
	defaultInterval   = 200 * time.Millisecond
	defaultBatchSize  = 100
	defaultLeaseTTL   = 30 * time.Second
	defaultBackoffMin = 500 * time.Millisecond
	defaultBackoffMax = 30 * time.Second

	// quietPeriod throttles the "outbox is empty" record. An idle relay ticks
	// several times a second, and a log line per tick would bury everything
	// else; one line every half minute is enough to tell idle from stuck.
	quietPeriod = 30 * time.Second
)

// Config tunes a Relay. Every zero value falls back to a sensible default, so
// the zero Config is usable.
type Config struct {
	// Interval is how often the relay drains the buckets it owns.
	Interval time.Duration

	// BatchSize caps how many rows one bucket gives up per round trip. A full
	// batch means there is more backlog, and the relay immediately asks again.
	BatchSize int

	// LeaseTTL is how long a bucket stays this instance's after a claim. It
	// bounds how long a bucket sits idle after an instance dies, and it is
	// renewed at a third of that period, so a slow tick or a brief network
	// hiccup does not hand the bucket to somebody else.
	LeaseTTL time.Duration

	// Owner identifies this relay instance in the lease table. Empty means a
	// fresh random identity per process, which is what a container wants: a
	// restarted instance must not inherit its predecessor's leases.
	Owner string

	// Logger receives the relay's structured records. Nil means silence.
	Logger Logger
}

// RelayOption configures additive relay features without adding fields to
// Config. Keeping Config's original five-field shape preserves source
// compatibility for callers that used an unkeyed composite literal.
type RelayOption func(*relayOptions)

type relayOptions struct {
	backoffMin    time.Duration
	backoffMax    time.Duration
	observer      Observer
	observerQueue int
}

// WithRetryBackoff changes the exponential full-jitter bounds. Non-positive
// values keep the defaults (500ms and 30s); max is raised to min when needed.
func WithRetryBackoff(min, max time.Duration) RelayOption {
	return func(options *relayOptions) {
		options.backoffMin = min
		options.backoffMax = max
	}
}

// WithObserver enables asynchronous, non-blocking observations. Relay queues
// a bounded number and drops excess observations rather than allowing a slow
// metrics backend to stall publishing.
func WithObserver(observer Observer) RelayOption {
	return func(options *relayOptions) { options.observer = observer }
}

// WithObserverQueue changes the bounded observation queue size. Values below
// one keep the default of 256.
func WithObserverQueue(size int) RelayOption {
	return func(options *relayOptions) { options.observerQueue = size }
}

type bucketRetry struct {
	consecutive int
	until       time.Time
}

type observerEvent struct {
	publish *PublishObservation
	backoff *BackoffObservation
}

// asyncObserver is intentionally lossy. One worker bounds both goroutine count
// and observer concurrency; a blocked observer can consume at most that worker
// while the relay keeps publishing and drops a full queue.
type asyncObserver struct {
	target Observer
	queue  chan observerEvent
	done   chan struct{}
	stop   sync.Once

	dropped atomic.Uint64
	panics  atomic.Uint64
}

// Relay publishes an outbox to a broker. Create it with NewRelay and run it in
// its own goroutine.
type Relay struct {
	store      Store
	publisher  Publisher
	interval   time.Duration
	batchSize  int
	leaseTTL   time.Duration
	owner      string
	log        Logger
	observer   *asyncObserver
	backoffMin time.Duration
	backoffMax time.Duration

	mu           sync.RWMutex
	owned        []int
	lastEmptyLog time.Time
	retries      map[int]bucketRetry
	leaseUntil   time.Time
	leaseCtx     context.Context
	leaseCancel  context.CancelFunc
	leaseTimer   *time.Timer
	leaseEpoch   uint64

	// Kept as fields to make retry timing deterministic in unit tests without
	// changing the public configuration surface.
	now    func() time.Time
	jitter func(time.Duration) time.Duration
}

// NewRelay builds a relay over store, publishing through publisher.
func NewRelay(store Store, publisher Publisher, cfg Config) *Relay {
	return NewRelayWithOptions(store, publisher, cfg)
}

// NewRelayWithOptions builds a relay with additive operational options. Use
// NewRelay when the defaults are sufficient.
func NewRelayWithOptions(store Store, publisher Publisher, cfg Config, options ...RelayOption) *Relay {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultLeaseTTL
	}
	if cfg.Owner == "" {
		cfg.Owner = randomOwner()
	}
	if cfg.Logger == nil {
		cfg.Logger = logx.Nop()
	}

	opts := relayOptions{
		backoffMin:    defaultBackoffMin,
		backoffMax:    defaultBackoffMax,
		observerQueue: 256,
	}
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	if opts.backoffMin <= 0 {
		opts.backoffMin = defaultBackoffMin
	}
	if opts.backoffMax <= 0 {
		opts.backoffMax = defaultBackoffMax
	}
	if opts.backoffMax < opts.backoffMin {
		opts.backoffMax = opts.backoffMin
	}
	if opts.observerQueue <= 0 {
		opts.observerQueue = 256
	}

	var observer *asyncObserver
	if opts.observer != nil {
		observer = newAsyncObserver(opts.observer, opts.observerQueue)
	}

	return &Relay{
		store:      store,
		publisher:  publisher,
		interval:   cfg.Interval,
		batchSize:  cfg.BatchSize,
		leaseTTL:   cfg.LeaseTTL,
		owner:      cfg.Owner,
		log:        cfg.Logger,
		observer:   observer,
		backoffMin: opts.backoffMin,
		backoffMax: opts.backoffMax,
		retries:    make(map[int]bucketRetry),
		now:        time.Now,
		jitter:     fullJitter,
	}
}

// Owner returns the identity this relay claims leases under.
func (r *Relay) Owner() string { return r.owner }

// DroppedObservations reports how many metric observations were discarded
// because the bounded asynchronous queue was full.
func (r *Relay) DroppedObservations() uint64 {
	if r.observer == nil {
		return 0
	}
	return r.observer.dropped.Load()
}

// ObserverPanics reports observer callback panics isolated by the asynchronous
// dispatcher. A panic never reaches the relay hot path.
func (r *Relay) ObserverPanics() uint64 {
	if r.observer == nil {
		return 0
	}
	return r.observer.panics.Load()
}

func newAsyncObserver(target Observer, queueSize int) *asyncObserver {
	observer := &asyncObserver{
		target: target,
		queue:  make(chan observerEvent, queueSize),
		done:   make(chan struct{}),
	}
	go observer.run()
	return observer
}

func (o *asyncObserver) run() {
	for {
		select {
		case <-o.done:
			return
		case event := <-o.queue:
			o.deliver(event)
		}
	}
}

func (o *asyncObserver) deliver(event observerEvent) {
	defer func() {
		if recover() != nil {
			o.panics.Add(1)
		}
	}()
	if event.publish != nil {
		o.target.ObservePublish(*event.publish)
		return
	}
	if event.backoff != nil {
		o.target.ObserveBackoff(*event.backoff)
	}
}

func (o *asyncObserver) enqueue(event observerEvent) {
	select {
	case <-o.done:
		return
	default:
	}
	select {
	case o.queue <- event:
	default:
		o.dropped.Add(1)
	}
}

func (o *asyncObserver) close() {
	o.stop.Do(func() { close(o.done) })
}

// Run polls until ctx is cancelled. It is meant to run in its own goroutine.
func (r *Relay) Run(ctx context.Context) {
	r.log.Info("outbox relay started",
		logx.F("owner", r.owner),
		logx.F("interval", r.interval.String()),
		logx.F("batchSize", r.batchSize),
		logx.F("leaseTTL", r.leaseTTL.String()),
	)
	if r.observer != nil {
		defer r.observer.close()
	}
	defer r.stopLease()

	r.claim(ctx)

	// Renewal has its own goroutine. A broker call may legitimately outlive one
	// TTL, and coupling renewal to the synchronous drain would silently hand the
	// bucket to another relay while the old owner continued its batch.
	renewCtx, stopRenew := context.WithCancel(ctx)
	defer stopRenew()
	go r.renewLoop(renewCtx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("outbox relay stopped", logx.F("owner", r.owner))
			return
		case <-ticker.C:
			r.drain(ctx)
		}
	}
}

func (r *Relay) renewLoop(ctx context.Context) {
	interval := r.renewInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.claim(ctx)
		}
	}
}

func (r *Relay) renewInterval() time.Duration {
	interval := r.leaseTTL / 3
	if interval <= 0 {
		return r.leaseTTL
	}
	return interval
}

// claim takes every free or expired bucket and renews the ones already held.
func (r *Relay) claim(ctx context.Context) {
	started := r.now()
	claimCtx, cancel := context.WithTimeout(ctx, r.renewInterval())
	defer cancel()
	owned, err := r.store.ClaimBuckets(claimCtx, r.owner, r.leaseTTL)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// Keep the previous set only until its local fencing deadline. The timer
		// cancels in-flight publish contexts and clears ownership when the last
		// successful renewal actually expires.
		r.log.Error("claim outbox buckets failed", logx.F("error", err.Error()))
		return
	}

	r.mu.Lock()
	changed := !slices.Equal(owned, r.owned)
	// One lease context fences the whole owned set. If even one bucket is lost,
	// cancel the old generation before publishing under the new set; otherwise
	// an in-flight publish for the removed bucket would inherit the renewed TTL.
	// Cancelling unchanged buckets too is conservative: their next drain starts
	// immediately under the new generation.
	if changed {
		r.cancelLeaseLocked()
	}
	r.owned = append(r.owned[:0], owned...)
	if len(owned) == 0 {
		r.cancelLeaseLocked()
	} else {
		r.renewLeaseLocked(started.Add(r.leaseTTL))
	}
	r.mu.Unlock()

	if changed {
		r.log.Info("outbox buckets claimed", logx.F("buckets", owned))
	}
}

func (r *Relay) renewLeaseLocked(until time.Time) {
	if r.leaseCtx == nil || r.leaseCtx.Err() != nil {
		r.leaseCtx, r.leaseCancel = context.WithCancel(context.Background())
	}
	r.leaseUntil = until
	r.leaseEpoch++
	epoch := r.leaseEpoch
	if r.leaseTimer != nil {
		r.leaseTimer.Stop()
	}
	delay := until.Sub(r.now())
	if delay < 0 {
		delay = 0
	}
	r.leaseTimer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if epoch != r.leaseEpoch {
			return
		}
		r.owned = nil
		r.cancelLeaseLocked()
	})
}

func (r *Relay) cancelLeaseLocked() {
	if r.leaseTimer != nil {
		r.leaseTimer.Stop()
		r.leaseTimer = nil
	}
	if r.leaseCancel != nil {
		r.leaseCancel()
	}
	r.leaseCtx = nil
	r.leaseCancel = nil
	r.leaseUntil = time.Time{}
}

func (r *Relay) stopLease() {
	r.mu.Lock()
	r.owned = nil
	r.cancelLeaseLocked()
	r.mu.Unlock()
}

func (r *Relay) bucketLease(bucket int) (context.Context, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.leaseCtx == nil || !r.now().Before(r.leaseUntil) || !slices.Contains(r.owned, bucket) {
		return nil, false
	}
	return r.leaseCtx, true
}

// drain publishes the backlog of every owned bucket. Buckets are independent —
// a partition key maps to exactly one of them — so they are drained in
// parallel, one goroutine each. Within a bucket the order is strictly
// sequential, which is what keeps one key's events in the order they happened.
func (r *Relay) drain(ctx context.Context) {
	r.mu.RLock()
	owned := append([]int(nil), r.owned...)
	leaseValid := r.leaseCtx != nil && r.now().Before(r.leaseUntil)
	r.mu.RUnlock()

	if len(owned) == 0 || !leaseValid {
		return
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for _, bucket := range owned {
		wg.Add(1)
		go func(bucket int) {
			defer wg.Done()
			n := r.drainBucket(ctx, bucket)
			mu.Lock()
			total += n
			mu.Unlock()
		}(bucket)
	}
	wg.Wait()

	if total > 0 {
		r.log.Info("published outbox batch", logx.F("count", total))
		return
	}
	if r.quietTick() {
		r.log.Info("outbox is empty")
	}
}

// drainBucket publishes one bucket in batches until it runs dry or an error
// stops it; whatever is left is retried on the next tick.
func (r *Relay) drainBucket(ctx context.Context, bucket int) int {
	leaseCtx, ok := r.bucketLease(bucket)
	if !ok {
		return 0
	}
	if r.bucketBackingOff(bucket) {
		return 0
	}

	publishCtx, cancel := context.WithCancel(ctx)
	stopLeaseCancel := context.AfterFunc(leaseCtx, cancel)
	defer func() {
		stopLeaseCancel()
		cancel()
	}()

	published := 0
	for {
		n, err := r.store.PublishBucket(publishCtx, bucket, r.owner, r.batchSize, r.publish)
		published += n
		if err != nil {
			if publishCtx.Err() != nil {
				return published
			}

			class := ClassifyPublishError(err)
			fields := []LogField{
				logx.F("bucket", bucket),
				logx.F("published", n),
				logx.F("errorClass", class),
				logx.F("error", err.Error()),
			}
			if IsTerminalPublishError(err) {
				r.clearBucketRetry(bucket)
				r.log.Error("publish outbox bucket failed", fields...)
				return published
			}

			delay, consecutive := r.deferBucket(bucket, err)
			fields = append(fields,
				logx.F("retryDelay", delay.String()),
				logx.F("consecutiveFailures", consecutive),
			)
			r.log.Error("publish outbox bucket failed", fields...)
			if r.observer != nil {
				observation := BackoffObservation{
					Bucket:              bucket,
					Delay:               delay,
					ConsecutiveFailures: consecutive,
					ErrorClass:          class,
				}
				r.observer.enqueue(observerEvent{backoff: &observation})
			}
			return published
		}
		r.clearBucketRetry(bucket)
		// A short batch means this bucket's backlog is drained for now.
		if n < r.batchSize {
			return published
		}
	}
}

func (r *Relay) publish(ctx context.Context, ev Event) error {
	started := r.now()
	fields := []LogField{
		logx.F("outboxId", ev.ID),
		logx.F("bucket", ev.Bucket),
		logx.F("partitionKey", ev.PartitionKey),
	}
	if ev.Topic != "" {
		fields = append(fields, logx.F("topic", ev.Topic))
	}

	if err := r.publisher.Publish(ctx, ev); err != nil {
		class := ClassifyPublishError(err)
		duration := r.now().Sub(started)
		r.log.Error("outbox event publish failed", append(fields,
			logx.F("errorClass", class),
			logx.F("error", err.Error()))...)
		if r.observer != nil {
			observation := PublishObservation{
				Bucket:     ev.Bucket,
				Topic:      ev.Topic,
				Duration:   duration,
				ErrorClass: class,
			}
			r.observer.enqueue(observerEvent{publish: &observation})
		}
		return err
	}
	if r.observer != nil {
		observation := PublishObservation{
			Bucket:    ev.Bucket,
			Topic:     ev.Topic,
			Duration:  r.now().Sub(started),
			Succeeded: true,
		}
		r.observer.enqueue(observerEvent{publish: &observation})
	}
	r.log.Debug("outbox event published", fields...)
	return nil
}

// bucketBackingOff keeps a failed dependency from being called on every relay
// tick. The failure count remains until a successful Store round trip so the
// next failure advances the exponential cap instead of starting over.
func (r *Relay) bucketBackingOff(bucket int) bool {
	r.mu.RLock()
	retry, ok := r.retries[bucket]
	r.mu.RUnlock()
	return ok && r.now().Before(retry.until)
}

func (r *Relay) deferBucket(bucket int, err error) (time.Duration, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	retry := r.retries[bucket]
	retry.consecutive++

	cap := r.backoffMin
	for i := 1; i < retry.consecutive && cap < r.backoffMax; i++ {
		if cap > r.backoffMax/2 {
			cap = r.backoffMax
			break
		}
		cap *= 2
	}
	if cap > r.backoffMax {
		cap = r.backoffMax
	}

	delay := r.jitter(cap)
	if hinted := PublishRetryAfter(err); hinted > 0 {
		delay = hinted
	}
	retry.until = r.now().Add(delay)
	r.retries[bucket] = retry
	return delay, retry.consecutive
}

func (r *Relay) clearBucketRetry(bucket int) {
	r.mu.Lock()
	delete(r.retries, bucket)
	r.mu.Unlock()
}

func fullJitter(cap time.Duration) time.Duration {
	if cap <= 0 {
		return 0
	}
	// All production defaults are far below MaxInt64. The guard keeps a
	// pathological custom duration from overflowing when the upper bound is
	// made inclusive.
	if cap == time.Duration(1<<63-1) {
		return time.Duration(mathrand.Int64N(int64(cap)))
	}
	return time.Duration(mathrand.Int64N(int64(cap) + 1))
}

// quietTick reports whether enough time has passed to log that the queue is
// idle again.
func (r *Relay) quietTick() bool {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.lastEmptyLog.IsZero() && now.Sub(r.lastEmptyLog) < quietPeriod {
		return false
	}
	r.lastEmptyLog = now
	return true
}

// randomOwner mints a per-process lease identity. It avoids a UUID dependency
// on purpose: this package is imported by integrations that may want nothing
// else from the SDK.
func randomOwner() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A duplicated owner id costs a shared bucket, not lost rows, so a
		// clock-based fallback is preferable to refusing to start.
		return fmt.Sprintf("relay-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
