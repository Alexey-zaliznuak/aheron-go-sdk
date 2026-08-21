package outbox

import (
	"context"
	"errors"
	"fmt"
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
	mu   sync.Mutex
	got  []string
	fail map[string]error
}

func newRecorder() *recorder { return &recorder{fail: map[string]error{}} }

func (r *recorder) Publish(_ context.Context, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.fail[ev.ID]; ok {
		return err
	}
	r.got = append(r.got, ev.ID)
	return nil
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

func TestRelayKeepsBucketsWhenClaimFails(t *testing.T) {
	store := newFakeStore(0)
	rec := newRecorder()

	// A lease outlives a failed renewal by design, so a relay that cannot reach
	// the store must keep publishing what it already owns.
	cfg := fastConfig()
	cfg.LeaseTTL = 15 * time.Millisecond
	runRelay(t, store, rec, cfg)

	waitFor(t, "initial claim", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claims >= 1
	})

	store.mu.Lock()
	store.claimErr = errors.New("database unreachable")
	store.mu.Unlock()

	waitFor(t, "renewal attempts", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claims >= 3
	})

	store.enqueue(0, "x1")
	waitFor(t, "publishing despite failed renewal", func() bool { return len(rec.ids()) == 1 })
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

	other := NewRelay(newFakeStore(), newRecorder(), Config{})
	if r.Owner() == other.Owner() {
		t.Fatal("two relays must not share a generated owner identity")
	}
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
