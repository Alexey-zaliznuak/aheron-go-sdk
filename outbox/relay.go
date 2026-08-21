package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"
)

const (
	defaultInterval  = 200 * time.Millisecond
	defaultBatchSize = 100
	defaultLeaseTTL  = 30 * time.Second

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

// Relay publishes an outbox to a broker. Create it with NewRelay and run it in
// its own goroutine.
type Relay struct {
	store     Store
	publisher Publisher
	interval  time.Duration
	batchSize int
	leaseTTL  time.Duration
	owner     string
	log       Logger

	mu           sync.RWMutex
	owned        []int
	lastEmptyLog time.Time
}

// NewRelay builds a relay over store, publishing through publisher.
func NewRelay(store Store, publisher Publisher, cfg Config) *Relay {
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

	return &Relay{
		store:     store,
		publisher: publisher,
		interval:  cfg.Interval,
		batchSize: cfg.BatchSize,
		leaseTTL:  cfg.LeaseTTL,
		owner:     cfg.Owner,
		log:       cfg.Logger,
	}
}

// Owner returns the identity this relay claims leases under.
func (r *Relay) Owner() string { return r.owner }

// Run polls until ctx is cancelled. It is meant to run in its own goroutine.
func (r *Relay) Run(ctx context.Context) {
	r.log.Info("outbox relay started",
		logx.F("owner", r.owner),
		logx.F("interval", r.interval.String()),
		logx.F("batchSize", r.batchSize),
		logx.F("leaseTTL", r.leaseTTL.String()),
	)

	r.claim(ctx)

	// The lease renews on its own, slower clock. Renewing on the drain tick
	// instead would rewrite every lease row several times a second for no
	// reason, and skipping renewal altogether would let a healthy instance lose
	// buckets it is actively draining.
	leaseTicker := time.NewTicker(r.leaseTTL / 3)
	defer leaseTicker.Stop()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("outbox relay stopped", logx.F("owner", r.owner))
			return
		case <-leaseTicker.C:
			r.claim(ctx)
		case <-ticker.C:
			r.drain(ctx)
		}
	}
}

// claim takes every free or expired bucket and renews the ones already held.
func (r *Relay) claim(ctx context.Context) {
	owned, err := r.store.ClaimBuckets(ctx, r.owner, r.leaseTTL)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// Keep the previous set: the leases outlive a failed renewal by design,
		// so a transient error must not stall publishing.
		r.log.Error("claim outbox buckets failed", logx.F("error", err.Error()))
		return
	}

	r.mu.Lock()
	changed := len(owned) != len(r.owned)
	r.owned = owned
	r.mu.Unlock()

	if changed {
		r.log.Info("outbox buckets claimed", logx.F("buckets", owned))
	}
}

// drain publishes the backlog of every owned bucket. Buckets are independent —
// a partition key maps to exactly one of them — so they are drained in
// parallel, one goroutine each. Within a bucket the order is strictly
// sequential, which is what keeps one key's events in the order they happened.
func (r *Relay) drain(ctx context.Context) {
	r.mu.RLock()
	owned := r.owned
	r.mu.RUnlock()

	if len(owned) == 0 {
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
	published := 0
	for {
		n, err := r.store.PublishBucket(ctx, bucket, r.owner, r.batchSize, r.publish)
		published += n
		if err != nil {
			if ctx.Err() != nil {
				return published
			}
			r.log.Error("publish outbox bucket failed",
				logx.F("bucket", bucket),
				logx.F("published", n),
				logx.F("error", err.Error()))
			return published
		}
		// A short batch means this bucket's backlog is drained for now.
		if n < r.batchSize {
			return published
		}
	}
}

func (r *Relay) publish(ctx context.Context, ev Event) error {
	fields := []LogField{
		logx.F("outboxId", ev.ID),
		logx.F("bucket", ev.Bucket),
		logx.F("partitionKey", ev.PartitionKey),
	}
	if ev.Topic != "" {
		fields = append(fields, logx.F("topic", ev.Topic))
	}

	if err := r.publisher.Publish(ctx, ev); err != nil {
		r.log.Error("outbox event publish failed", append(fields, logx.F("error", err.Error()))...)
		return err
	}
	r.log.Debug("outbox event published", fields...)
	return nil
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
