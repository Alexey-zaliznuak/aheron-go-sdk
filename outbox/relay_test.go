package outbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory outbox: a per-bucket FIFO plus the lease bookkeeping
// the relay expects.
type fakeStore struct {
	mu sync.Mutex

	buckets  []int
	claimErr error
	claims   int

	pending map[int][]Event

	// failOn makes publish of an event with this ID fail, standing in for a
	// broker that rejects one message.
	failOn string
}

// fencedLeaseStore models the lease checks a durable Store must perform before
// and after each external publish. It lets the relay tests exercise ownership
// handoff without a database.
type fencedLeaseStore struct {
	mu sync.Mutex

	owner      string
	leaseUntil time.Time
	reject     map[string]bool
	pending    []Event
}

func newFencedLeaseStore(events ...Event) *fencedLeaseStore {
	return &fencedLeaseStore{reject: map[string]bool{}, pending: append([]Event(nil), events...)}
}

func (s *fencedLeaseStore) ClaimBuckets(ctx context.Context, owner string, lease time.Duration) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject[owner] {
		return nil, errors.New("lease store unavailable for owner")
	}
	now := time.Now()
	if s.owner != "" && s.owner != owner && s.leaseUntil.After(now) {
		return nil, nil
	}
	s.owner = owner
	s.leaseUntil = now.Add(lease)
	return []int{0}, nil
}

func (s *fencedLeaseStore) PublishBucket(
	ctx context.Context,
	bucket int,
	owner string,
	limit int,
	publish func(context.Context, Event) error,
) (int, error) {
	published := 0
	for published < limit {
		s.mu.Lock()
		if bucket != 0 || s.owner != owner || !s.leaseUntil.After(time.Now()) || len(s.pending) == 0 {
			s.mu.Unlock()
			return published, nil
		}
		event := s.pending[0]
		s.mu.Unlock()

		if err := publish(ctx, event); err != nil {
			return published, err
		}

		s.mu.Lock()
		fenced := ctx.Err() != nil || s.owner != owner || !s.leaseUntil.After(time.Now()) ||
			len(s.pending) == 0 || s.pending[0].ID != event.ID
		if fenced {
			s.mu.Unlock()
			return published, Transient(errors.New("lease lost after publish"), 0)
		}
		s.pending = s.pending[1:]
		s.mu.Unlock()
		published++
	}
	return published, nil
}

func (s *fencedLeaseStore) rejectOwner(owner string) {
	s.mu.Lock()
	s.reject[owner] = true
	s.mu.Unlock()
}

func newFakeStore(buckets ...int) *fakeStore {
	return &fakeStore{buckets: buckets, pending: map[int][]Event{}}
}

func (s *fakeStore) ClaimBuckets(_ context.Context, _ string, _ time.Duration) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return append([]int(nil), s.buckets...), nil
}

func (s *fakeStore) PublishBucket(ctx context.Context, bucket int, _ string, limit int,
	publish func(context.Context, Event) error) (int, error) {
	published := 0
	for published < limit {
		s.mu.Lock()
		queue := s.pending[bucket]
		if len(queue) == 0 {
			s.mu.Unlock()
			return published, nil
		}
		ev := queue[0]
		fail := s.failOn != "" && ev.ID == s.failOn
		s.mu.Unlock()

		if err := publish(ctx, ev); err != nil {
			return published, fmt.Errorf("publish %s: %w", ev.ID, err)
		}
		if fail {
			return published, fmt.Errorf("publish %s: rejected", ev.ID)
		}

		s.mu.Lock()
		s.pending[bucket] = s.pending[bucket][1:]
		s.mu.Unlock()
		published++
	}
	return published, nil
}

func (s *fakeStore) enqueue(bucket int, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.pending[bucket] = append(s.pending[bucket], Event{
			ID:           id,
			Bucket:       bucket,
			PartitionKey: id,
			Payload:      []byte(`{}`),
		})
	}
}

func (s *fakeStore) remaining(bucket int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending[bucket])
}

// recorder collects what the relay handed to the broker, in order.
type recorder struct {
	mu    sync.Mutex
	got   []string
	fail  map[string]error
	calls map[string]int
}

func newRecorder() *recorder {
	return &recorder{fail: map[string]error{}, calls: map[string]int{}}
}

func (r *recorder) Publish(_ context.Context, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[ev.ID]++
	if err, ok := r.fail[ev.ID]; ok {
		return err
	}
	r.got = append(r.got, ev.ID)
	return nil
}

func (r *recorder) callCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[id]
}

type observationRecorder struct {
	mu        sync.Mutex
	publishes []PublishObservation
	backoffs  []BackoffObservation
}

type blockingObserver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *blockingObserver) ObservePublish(PublishObservation) {
	o.once.Do(func() { close(o.started) })
	<-o.release
}

func (o *blockingObserver) ObserveBackoff(BackoffObservation) {
	o.once.Do(func() { close(o.started) })
	<-o.release
}

type panicObserver struct{}

func (panicObserver) ObservePublish(PublishObservation) { panic("metrics backend panic") }
func (panicObserver) ObserveBackoff(BackoffObservation) { panic("metrics backend panic") }

func (r *observationRecorder) ObservePublish(observation PublishObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishes = append(r.publishes, observation)
}

func (r *observationRecorder) ObserveBackoff(observation BackoffObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backoffs = append(r.backoffs, observation)
}

func (r *observationRecorder) snapshot() ([]PublishObservation, []BackoffObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PublishObservation(nil), r.publishes...), append([]BackoffObservation(nil), r.backoffs...)
}

func (r *recorder) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// fastConfig keeps a test relay ticking quickly enough to finish in
// milliseconds while leaving the lease clock (a third of LeaseTTL) slower than
// the drain clock, as it is in production.
func fastConfig() Config {
	return Config{Interval: 2 * time.Millisecond, LeaseTTL: 300 * time.Millisecond, BatchSize: 2}
}

func runRelay(t *testing.T, store Store, pub Publisher, cfg Config) context.CancelFunc {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRelay(store, pub, cfg).Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("relay did not stop after cancel")
		}
	})
	return cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func grantLocalLease(t *testing.T, relay *Relay, buckets ...int) {
	t.Helper()
	relay.mu.Lock()
	relay.owned = append([]int(nil), buckets...)
	relay.renewLeaseLocked(relay.now().Add(relay.leaseTTL))
	relay.mu.Unlock()
	t.Cleanup(func() {
		relay.stopLease()
		if relay.observer != nil {
			relay.observer.close()
		}
	})
}

func TestRelayDrainsEveryOwnedBucket(t *testing.T) {
	store := newFakeStore(0, 1, 2)
	store.enqueue(0, "a1", "a2", "a3")
	store.enqueue(1, "b1")
	store.enqueue(2, "c1", "c2")

	rec := newRecorder()
	runRelay(t, store, rec, fastConfig())

	waitFor(t, "all buckets drained", func() bool { return len(rec.ids()) == 6 })

	// BatchSize is 2 and bucket 0 holds three rows, so this also covers the
	// loop that asks again after a full batch.
	for _, bucket := range []int{0, 1, 2} {
		if n := store.remaining(bucket); n != 0 {
			t.Fatalf("bucket %d still holds %d rows", bucket, n)
		}
	}
}

func TestRelayKeepsOrderWithinBucket(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1", "2", "3", "4", "5")

	rec := newRecorder()
	runRelay(t, store, rec, fastConfig())

	waitFor(t, "bucket drained", func() bool { return len(rec.ids()) == 5 })

	want := []string{"1", "2", "3", "4", "5"}
	got := rec.ids()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
}

func TestRelayStopsBucketAtFirstFailure(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1", "2", "3")

	rec := newRecorder()
	rec.fail["2"] = errors.New("broker rejected")

	runRelay(t, store, rec, fastConfig())

	// "1" goes out; "2" fails forever, so "3" must never be published ahead of
	// it — that is the ordering guarantee the pattern rests on.
	waitFor(t, "first event published", func() bool { return len(rec.ids()) >= 1 })
	time.Sleep(50 * time.Millisecond)

	for _, id := range rec.ids() {
		if id == "3" {
			t.Fatalf("published %v: event behind a failing one must wait", rec.ids())
		}
	}
	if n := store.remaining(0); n != 2 {
		t.Fatalf("bucket 0 holds %d rows, want 2 (the failing one and the one behind it)", n)
	}
}

func TestRelayKeepsBucketsOnlyUntilFailedRenewalExpires(t *testing.T) {
	store := newFakeStore(0)
	rec := newRecorder()

	// A transient failed renewal does not stop a still-live lease immediately,
	// but the local fence must stop it at the last confirmed deadline.
	cfg := fastConfig()
	cfg.LeaseTTL = 300 * time.Millisecond
	runRelay(t, store, rec, cfg)

	waitFor(t, "initial claim", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claims >= 1
	})

	store.mu.Lock()
	store.claimErr = errors.New("database unreachable")
	store.mu.Unlock()

	store.enqueue(0, "x1")
	waitFor(t, "publishing despite failed renewal", func() bool { return len(rec.ids()) == 1 })

	time.Sleep(cfg.LeaseTTL + 20*time.Millisecond)
	store.enqueue(0, "x2")
	time.Sleep(30 * time.Millisecond)
	if got := rec.ids(); !slices.Equal(got, []string{"x1"}) {
		t.Fatalf("published after the last confirmed lease expired: %v", got)
	}
}

func TestLeaseRenewsDuringLongPublishAndFencesOldOwnerAfterHandoff(t *testing.T) {
	store := newFencedLeaseStore(
		Event{ID: "1", Bucket: 0, PartitionKey: "subject", Payload: []byte(`{"n":1}`)},
		Event{ID: "2", Bucket: 0, PartitionKey: "subject", Payload: []byte(`{"n":2}`)},
	)
	const leaseTTL = 90 * time.Millisecond

	firstStarted := make(chan struct{})
	firstReturned := make(chan struct{})
	releaseFirst := make(chan struct{})
	var (
		callsMu sync.Mutex
		callsA  []string
	)
	publisherA := PublisherFunc(func(_ context.Context, event Event) error {
		callsMu.Lock()
		callsA = append(callsA, event.ID)
		callsMu.Unlock()
		if event.ID == "1" {
			select {
			case <-firstStarted:
			default:
				close(firstStarted)
			}
			<-releaseFirst // deliberately ignores cancellation to exercise Store fencing
			close(firstReturned)
		}
		return nil
	})
	publisherB := newRecorder()

	configA := Config{Interval: 5 * time.Millisecond, BatchSize: 2, LeaseTTL: leaseTTL, Owner: "owner-a"}
	configB := Config{Interval: 5 * time.Millisecond, BatchSize: 2, LeaseTTL: leaseTTL, Owner: "owner-b"}
	relayA := NewRelay(store, publisherA, configA)
	relayB := NewRelay(store, publisherB, configB)

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	go func() { defer close(doneA); relayA.Run(ctxA) }()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("owner-a did not start its long publish")
	}
	go func() { defer close(doneB); relayB.Run(ctxB) }()

	// The publish is longer than a TTL. Independent renewal must nevertheless
	// keep owner-b from acquiring the bucket.
	time.Sleep(2 * leaseTTL)
	if got := publisherB.ids(); len(got) != 0 {
		t.Fatalf("owner-b published %v while owner-a was renewing during a long publish", got)
	}

	// Simulate owner-a losing access to the lease store. Its local lease
	// deadline cancels the in-flight context; after actual expiry owner-b takes
	// the bucket and drains it.
	store.rejectOwner("owner-a")
	waitFor(t, "owner-b handoff drain", func() bool { return len(publisherB.ids()) == 2 })
	close(releaseFirst)

	select {
	case <-firstReturned:
	case <-time.After(time.Second):
		t.Fatal("owner-a long publish did not return")
	}
	time.Sleep(2 * configA.Interval)
	callsMu.Lock()
	gotA := append([]string(nil), callsA...)
	callsMu.Unlock()
	if !slices.Equal(gotA, []string{"1"}) {
		t.Fatalf("old owner continued its batch after handoff: calls=%v", gotA)
	}

	cancelA()
	cancelB()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("relay-a did not stop")
	}
	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("relay-b did not stop")
	}
}

func TestPartialOwnershipChangeCancelsRemovedBucketPublish(t *testing.T) {
	store := newFakeStore(0, 1)
	store.enqueue(0, "bucket-0-event")

	started := make(chan struct{})
	returned := make(chan struct{})
	publisher := PublisherFunc(func(ctx context.Context, _ Event) error {
		close(started)
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})
	relay := NewRelay(store, publisher, Config{Owner: "owner-a", LeaseTTL: time.Minute})
	t.Cleanup(relay.stopLease)

	relay.claim(context.Background())
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		relay.drainBucket(context.Background(), 0)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("removed bucket publish did not start")
	}

	store.mu.Lock()
	store.buckets = []int{1}
	store.mu.Unlock()
	relay.claim(context.Background())

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("partial ownership change did not cancel removed bucket publish")
	}
	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("removed bucket drain did not stop after fencing")
	}
	if _, ok := relay.bucketLease(0); ok {
		t.Fatal("removed bucket retained the new lease generation")
	}
	if _, ok := relay.bucketLease(1); !ok {
		t.Fatal("retained bucket did not receive the new lease generation")
	}
}

func TestRelayPublishesNothingWithoutBuckets(t *testing.T) {
	store := newFakeStore()
	store.enqueue(0, "orphan")

	rec := newRecorder()
	runRelay(t, store, rec, fastConfig())

	time.Sleep(50 * time.Millisecond)
	if got := rec.ids(); len(got) != 0 {
		t.Fatalf("published %v while owning no bucket", got)
	}
}

func TestNewRelayFillsDefaults(t *testing.T) {
	r := NewRelay(newFakeStore(), newRecorder(), Config{})

	if r.interval != defaultInterval || r.batchSize != defaultBatchSize || r.leaseTTL != defaultLeaseTTL {
		t.Fatalf("defaults not applied: %v %d %v", r.interval, r.batchSize, r.leaseTTL)
	}
	if r.Owner() == "" {
		t.Fatal("owner must default to a generated identity")
	}
	if r.log == nil {
		t.Fatal("logger must default to a no-op rather than nil")
	}
	if r.backoffMin != defaultBackoffMin || r.backoffMax != defaultBackoffMax {
		t.Fatalf("backoff defaults not applied: %v %v", r.backoffMin, r.backoffMax)
	}

	other := NewRelay(newFakeStore(), newRecorder(), Config{})
	if r.Owner() == other.Owner() {
		t.Fatal("two relays must not share a generated owner identity")
	}
}

func TestConfigRetainsLegacyUnkeyedShape(t *testing.T) {
	// This compile-time guard is intentional: adding a sixth field would break
	// callers that used Config as an unkeyed literal in a minor release.
	_ = Config{time.Second, 10, time.Minute, "owner", nil}
}

func TestRelayBacksOffTransientBucketAndReportsMetrics(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1")
	rec := newRecorder()
	rec.fail["1"] = Transient(errors.New("broker unavailable"), 0)
	observations := &observationRecorder{}

	r := NewRelayWithOptions(store, rec, Config{Owner: "owner-a"},
		WithRetryBackoff(100*time.Millisecond, time.Second),
		WithObserver(observations),
	)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	grantLocalLease(t, r, 0)
	r.jitter = func(cap time.Duration) time.Duration { return cap }

	if got := r.drainBucket(context.Background(), 0); got != 0 {
		t.Fatalf("published %d rows on failure", got)
	}
	if calls := rec.callCount("1"); calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", calls)
	}

	// Many relay ticks during the delay must not hammer the dependency.
	for range 20 {
		r.drainBucket(context.Background(), 0)
	}
	if calls := rec.callCount("1"); calls != 1 {
		t.Fatalf("publisher calls during backoff = %d, want 1", calls)
	}

	now = now.Add(100 * time.Millisecond)
	r.drainBucket(context.Background(), 0)
	if calls := rec.callCount("1"); calls != 2 {
		t.Fatalf("publisher calls after backoff = %d, want 2", calls)
	}

	waitFor(t, "observer delivery", func() bool {
		publishes, backoffs := observations.snapshot()
		return len(publishes) == 2 && len(backoffs) == 2
	})
	publishes, backoffs := observations.snapshot()
	if len(publishes) != 2 || publishes[0].ErrorClass != PublishErrorTransient {
		t.Fatalf("publish observations = %+v", publishes)
	}
	if len(backoffs) != 2 {
		t.Fatalf("backoff observations = %+v, want 2", backoffs)
	}
	if backoffs[0].Delay != 100*time.Millisecond || backoffs[0].ConsecutiveFailures != 1 {
		t.Fatalf("first backoff = %+v", backoffs[0])
	}
	if backoffs[1].Delay != 200*time.Millisecond || backoffs[1].ConsecutiveFailures != 2 {
		t.Fatalf("second backoff = %+v", backoffs[1])
	}
}

func TestRelayHonorsRetryAfter(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1")
	rec := newRecorder()
	rec.fail["1"] = Transient(errors.New("rate limited"), 7*time.Second)

	r := NewRelay(store, rec, Config{Owner: "owner-a"})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	grantLocalLease(t, r, 0)
	r.jitter = func(time.Duration) time.Duration { return time.Millisecond }

	r.drainBucket(context.Background(), 0)
	r.mu.RLock()
	retry := r.retries[0]
	r.mu.RUnlock()
	if got := retry.until.Sub(now); got != 7*time.Second {
		t.Fatalf("retry delay = %v, want Retry-After 7s", got)
	}
}

func TestRelayDoesNotBackOffDeadLetteredFailure(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1")
	rec := newRecorder()
	rec.fail["1"] = &DeadLetteredPublishError{EventID: "1", Err: Permanent(errors.New("invalid payload"))}
	observations := &observationRecorder{}

	r := NewRelayWithOptions(store, rec, Config{Owner: "owner-a"}, WithObserver(observations))
	grantLocalLease(t, r, 0)
	r.drainBucket(context.Background(), 0)

	r.mu.RLock()
	_, backedOff := r.retries[0]
	r.mu.RUnlock()
	if backedOff {
		t.Fatal("permanent failure opened bucket backoff")
	}
	_, backoffs := observations.snapshot()
	if len(backoffs) != 0 {
		t.Fatalf("terminal failure emitted backoff observations: %+v", backoffs)
	}
}

func TestRelayBacksOffPermanentWithoutStoreConfirmation(t *testing.T) {
	store := newFakeStore(0)
	store.enqueue(0, "1")
	rec := newRecorder()
	rec.fail["1"] = Permanent(errors.New("invalid payload"))

	r := NewRelayWithOptions(store, rec, Config{Owner: "owner-a"},
		WithRetryBackoff(time.Second, time.Second))
	grantLocalLease(t, r, 0)
	r.jitter = func(cap time.Duration) time.Duration { return cap }
	r.drainBucket(context.Background(), 0)

	r.mu.RLock()
	_, backedOff := r.retries[0]
	r.mu.RUnlock()
	if !backedOff {
		t.Fatal("publisher-level permanent error was treated as terminal without Store confirmation")
	}
}

func TestObserverCannotBlockRelayAndQueueIsBounded(t *testing.T) {
	observer := &blockingObserver{started: make(chan struct{}), release: make(chan struct{})}
	r := NewRelayWithOptions(newFakeStore(), newRecorder(), Config{},
		WithObserver(observer), WithObserverQueue(1))
	t.Cleanup(func() {
		close(observer.release)
		r.observer.close()
	})

	if err := r.publish(context.Background(), Event{ID: "first"}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("observer worker did not start")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_ = r.publish(context.Background(), Event{ID: fmt.Sprintf("event-%d", i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("blocked observer stalled relay publish path")
	}
	if dropped := r.DroppedObservations(); dropped == 0 {
		t.Fatal("bounded observer queue did not report dropped observations")
	}
}

func TestObserverPanicIsIsolatedAndWorkerContinues(t *testing.T) {
	r := NewRelayWithOptions(newFakeStore(), newRecorder(), Config{}, WithObserver(panicObserver{}))
	t.Cleanup(r.observer.close)

	if err := r.publish(context.Background(), Event{ID: "first"}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	waitFor(t, "first isolated observer panic", func() bool { return r.ObserverPanics() >= 1 })
	if err := r.publish(context.Background(), Event{ID: "second"}); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	waitFor(t, "second isolated observer panic", func() bool { return r.ObserverPanics() >= 2 })
}

func TestPublisherFuncSatisfiesPublisher(t *testing.T) {
	var got Event
	var p Publisher = PublisherFunc(func(_ context.Context, ev Event) error {
		got = ev
		return nil
	})
	if err := p.Publish(context.Background(), Event{ID: "1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got.ID != "1" {
		t.Fatalf("got %+v", got)
	}
}
