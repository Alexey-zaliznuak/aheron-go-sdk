package readcache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func cacheFor(t *testing.T, addr, namespace string) *Cache {
	t.Helper()
	c, err := New(Config{URL: "redis://" + addr, Namespace: namespace, Lease: 150 * time.Millisecond, OperationTimeout: 100 * time.Millisecond, LoadTimeout: 3 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitFor(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for loader")
	}
}

func TestDistributedStampedeAndCopyIsolation(t *testing.T) {
	s := miniredis.RunT(t)
	clients := []*Cache{cacheFor(t, s.Addr(), "test"), cacheFor(t, s.Addr(), "test"), cacheFor(t, s.Addr(), "test")}
	var loads atomic.Int32
	load := func(ctx context.Context) (map[string][]int, error) {
		loads.Add(1)
		time.Sleep(60 * time.Millisecond)
		return map[string][]int{"value": {7}}, nil
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := Get(context.Background(), clients[i%3], "project-a:contract:v1", time.Minute, load)
			if err != nil {
				t.Error(err)
				return
			}
			if v["value"][0] != 7 {
				t.Errorf("shared value mutated: %v", v)
			}
			v["value"][0] = 99
		}(i)
	}
	close(start)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("source loads=%d, want 1", loads.Load())
	}
	if _, err := Get(context.Background(), clients[0], "project-b:contract:v1", time.Minute, load); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 {
		t.Fatal("tenant key collision")
	}
}

func TestInvalidateFencesInFlightFill(t *testing.T) {
	s := miniredis.RunT(t)
	a := cacheFor(t, s.Addr(), "test")
	b := cacheFor(t, s.Addr(), "test")
	started, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var loads atomic.Int32
	go func() {
		defer close(done)
		v, err := Get(context.Background(), a, "k", time.Minute, func(context.Context) (string, error) {
			if loads.Add(1) == 1 {
				close(started)
				<-proceed
				return "old", nil
			}
			return "new", nil
		})
		if err != nil || v != "new" {
			t.Errorf("late loader returned %q %v", v, err)
		}
	}()
	waitFor(t, started)
	if err := b.Invalidate(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	v, err := Get(context.Background(), b, "k", time.Minute, func(context.Context) (string, error) { return "new", nil })
	if err != nil || v != "new" {
		t.Fatal(v, err)
	}
	close(proceed)
	waitFor(t, done)
	v, err = Get(context.Background(), b, "k", time.Minute, func(context.Context) (string, error) { t.Error("cache overwritten/missing"); return "wrong", nil })
	if err != nil || v != "new" {
		t.Fatal(v, err)
	}
}

func TestLostLeaseCannotOverwriteOrUnlockNewOwner(t *testing.T) {
	s := miniredis.RunT(t)
	a := cacheFor(t, s.Addr(), "test")
	valueKey, lockKey := a.keys("k")
	ctx := context.Background()
	if err := a.client.Set(ctx, lockKey, "new-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := a.run(ctx, publish, []string{valueKey, lockKey}, "old-owner", "stale", 60000).Int(); err != nil || got != 0 {
		t.Fatal(got, err)
	}
	if got, err := a.run(ctx, release, []string{lockKey}, "old-owner").Int(); err != nil || got != 0 {
		t.Fatal(got, err)
	}
	if got, _ := s.Get(lockKey); got != "new-owner" {
		t.Fatal("new lease removed")
	}
	if s.Exists(valueKey) {
		t.Fatal("stale data published")
	}
}

func TestLeaseRenewsDuringSlowFill(t *testing.T) {
	s := miniredis.RunT(t)
	a := cacheFor(t, s.Addr(), "test")
	started, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, err := Get(context.Background(), a, "k", time.Minute, func(context.Context) (string, error) { close(started); <-proceed; return "ok", nil })
		if err != nil {
			t.Error(err)
		}
	}()
	waitFor(t, started)
	_, lockKey := a.keys("k")
	s.FastForward(100 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for s.TTL(lockKey) < 100*time.Millisecond && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.TTL(lockKey) < 100*time.Millisecond {
		t.Fatal("lease not renewed")
	}
	close(proceed)
	waitFor(t, done)
}

func TestCanceledLeaderDoesNotCancelWaiter(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	started, proceed := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	var loads atomic.Int32
	load := func(ctx context.Context) (string, error) {
		loads.Add(1)
		close(started)
		<-proceed
		return "ok", ctx.Err()
	}
	go func() {
		defer close(leaderDone)
		_, err := Get(ctx, c, "k", time.Minute, load)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("leader error %v", err)
		}
	}()
	waitFor(t, started)
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		v, err := Get(context.Background(), c, "k", time.Minute, load)
		if v != "ok" || err != nil {
			t.Errorf("waiter %q %v", v, err)
		}
	}()
	cancel()
	waitFor(t, leaderDone)
	close(proceed)
	waitFor(t, waiterDone)
	if loads.Load() != 1 {
		t.Fatal("canceled leader caused duplicate fill")
	}
}

func TestErrorsNeverCachedAndLockReleased(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	sourceErr := errors.New("source unavailable")
	_, err := Get(context.Background(), c, "k", time.Minute, func(context.Context) (string, error) { return "", sourceErr })
	if !errors.Is(err, sourceErr) {
		t.Fatal(err)
	}
	v, err := Get(context.Background(), c, "k", time.Minute, func(context.Context) (string, error) { return "recovered", nil })
	if v != "recovered" || err != nil {
		t.Fatal(v, err)
	}
	_, lockKey := c.keys("k")
	if s.Exists(lockKey) {
		t.Fatal("successful fill retained lock")
	}
}

func TestSlowSourceCannotExtendMutableFreshnessBudget(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	value, err := Get(context.Background(), c, "slow", 20*time.Millisecond, func(context.Context) (string, error) {
		time.Sleep(35 * time.Millisecond)
		return "source snapshot", nil
	})
	if err != nil || value != "source snapshot" {
		t.Fatal(value, err)
	}
	key, _ := c.keys("slow")
	if s.Exists(key) {
		t.Fatal("slow source received a new TTL after its freshness deadline")
	}
}

func TestUnencodableValuesDoNotShareMutableReferences(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	type unsupported struct {
		Items   []int
		Channel chan int
	}
	load := func(context.Context) (unsupported, error) {
		time.Sleep(10 * time.Millisecond)
		return unsupported{Items: []int{7}, Channel: make(chan int)}, nil
	}
	var values [12]unsupported
	var wg sync.WaitGroup
	for i := range values {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := Get(context.Background(), c, "unsupported", time.Minute, load)
			if err != nil {
				t.Error(err)
				return
			}
			values[i] = v
		}(i)
	}
	wg.Wait()
	for i := range values {
		values[i].Items[0] = i
	}
	for i := range values {
		if values[i].Items[0] != i {
			t.Fatal("uncacheable fallback shared a mutable slice")
		}
	}
	key, _ := c.keys("unsupported")
	if s.Exists(key) {
		t.Fatal("unsupported value cached")
	}
}

func TestDynamicJSONNumbersPreservePrecision(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	load := func(context.Context) (map[string]any, error) {
		return map[string]any{"nested": map[string]any{"id": json.Number("9007199254740993")}}, nil
	}
	for range 2 {
		v, err := Get(context.Background(), c, "number", time.Minute, load)
		if err != nil {
			t.Fatal(err)
		}
		if got := v["nested"].(map[string]any)["id"]; got != json.Number("9007199254740993") {
			t.Fatalf("number lost precision: %T %v", got, got)
		}
	}
}

func TestExpiryNamespaceAndCorruptValues(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "a")
	other := cacheFor(t, s.Addr(), "b")
	var loads atomic.Int32
	load := func(context.Context) (int32, error) { return loads.Add(1), nil }
	if v, _ := Get(context.Background(), c, "k", time.Second, load); v != 1 {
		t.Fatal(v)
	}
	if v, _ := Get(context.Background(), other, "k", time.Second, load); v != 2 {
		t.Fatal("namespace collision", v)
	}
	s.FastForward(time.Second)
	if v, _ := Get(context.Background(), c, "k", time.Second, load); v != 3 {
		t.Fatal("ttl did not expire", v)
	}
	valueKey, _ := c.keys("k")
	_ = s.Set(valueKey, "invalid-json")
	if v, err := Get(context.Background(), c, "k", time.Second, load); err != nil || v != 4 {
		t.Fatal(v, err)
	}
	wrong, _ := json.Marshal(payload{Value: json.RawMessage(`"wrong-type"`), Until: time.Now().Add(time.Minute).UnixNano()})
	_ = s.Set(valueKey, string(wrong))
	if v, err := Get(context.Background(), c, "k", time.Second, load); err != nil || v != 5 {
		t.Fatal(v, err)
	}
}

func TestAbsoluteDeadlineAndExpiredRecords(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	type token struct {
		Expiry time.Time
		Body   string
	}
	until := time.Now().Add(time.Minute)
	deadline := func(v token) time.Time { return v.Expiry }
	_, err := GetUntil(context.Background(), c, "token", deadline, func(context.Context) (token, error) { return token{until, "body"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	key, _ := c.keys("token")
	// miniredis TTL does not tick with wall time. Verify the encoded absolute
	// deadline, which is also checked on reads if server/client clocks differ.
	body, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var stored payload
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Until > until.UnixNano() {
		t.Fatal("cache outlives token")
	}
	_, err = GetUntil(context.Background(), c, "expired", deadline, func(context.Context) (token, error) { return token{time.Now().Add(-time.Second), "body"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	key, _ = c.keys("expired")
	if s.Exists(key) {
		t.Fatal("expired token cached")
	}
}

func TestTaggedAliasesInvalidatedAcrossInstances(t *testing.T) {
	s := miniredis.RunT(t)
	a := cacheFor(t, s.Addr(), "test")
	b := cacheFor(t, s.Addr(), "test")
	var current atomic.Int32
	current.Store(1)
	load := func(context.Context) (int32, error) { return current.Load(), nil }
	for _, key := range []string{"channel:id", "channel:external"} {
		if v, err := GetTagged(context.Background(), a, key, time.Minute, []string{"project:1", "channel:1"}, load); err != nil || v != 1 {
			t.Fatal(v, err)
		}
	}
	current.Store(2)
	if err := b.InvalidateTags(context.Background(), "channel:1"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"channel:id", "channel:external"} {
		if v, err := GetTagged(context.Background(), a, key, time.Minute, []string{"channel:1", "project:1", "channel:1"}, load); err != nil || v != 2 {
			t.Fatal(v, err)
		}
	}
}

func TestTaggedLateFillAndGenerationLoss(t *testing.T) {
	s := miniredis.RunT(t)
	a := cacheFor(t, s.Addr(), "test")
	b := cacheFor(t, s.Addr(), "test")
	started, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		v, err := GetTagged(context.Background(), a, "alias", time.Minute, []string{"tag"}, func(context.Context) (int, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-proceed
				return 1, nil
			}
			return 2, nil
		})
		if err != nil || v != 2 {
			t.Errorf("tag invalidation lost: %v %v", v, err)
		}
	}()
	waitFor(t, started)
	if err := b.InvalidateTags(context.Background(), "tag"); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	waitFor(t, done)
	// Losing a generation must allocate a new random generation, never reuse 0.
	s.Del(a.tagKeys([]string{"tag"})[0])
	if v, err := GetTagged(context.Background(), a, "alias", time.Minute, []string{"tag"}, func(context.Context) (int, error) { return 3, nil }); err != nil || v != 3 {
		t.Fatal(v, err)
	}
}

func TestRedisFailureFallsBackWithLocalSingleflight(t *testing.T) {
	s := miniredis.RunT(t)
	c := cacheFor(t, s.Addr(), "test")
	s.SetError("unavailable")
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := Get(context.Background(), c, "k", time.Minute, func(context.Context) (int, error) { calls.Add(1); time.Sleep(50 * time.Millisecond); return 42, nil })
			if err != nil || v != 42 {
				t.Error(v, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("outage stampede", calls.Load())
	}
	if err := c.InvalidateTags(context.Background(), "t"); err == nil {
		t.Fatal("invalidation failure hidden")
	}
	if c.Statistics().Failures == 0 {
		t.Fatal("failure metric not incremented")
	}
}

func TestNilDisabledAndInvalidConfiguration(t *testing.T) {
	if err := (*Cache)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{URL: "broken://user:secret@host"}, {Lease: -1}, {Namespace: "bad\nname"}} {
		if _, err := New(cfg, nil); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	c, err := New(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Fatal("empty URL must disable all cache overhead")
	}
	defer c.Close()
	for _, c := range []*Cache{nil, c} {
		if v, err := Get(context.Background(), c, "k", time.Minute, func(context.Context) (int, error) { return 7, nil }); err != nil || v != 7 {
			t.Fatal(v, err)
		}
	}
}

// Run with an isolated namespace against a real deployment. The URL is supplied
// only through the environment and is never emitted in test output.
func TestRedisLive(t *testing.T) {
	url := os.Getenv("READCACHE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("READCACHE_TEST_REDIS_URL not set")
	}
	c, err := New(Config{URL: url, Namespace: "aheron:readcache-test:" + nonce()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var calls atomic.Int32
	load := func(context.Context) (string, error) { calls.Add(1); return "ok", nil }
	for i := 0; i < 2; i++ {
		v, err := GetTagged(context.Background(), c, "probe", time.Second, []string{"probe"}, load)
		if v != "ok" || err != nil {
			t.Fatal(v, err)
		}
	}
	if calls.Load() != 1 || c.Statistics().Failures != 0 {
		t.Fatalf("Redis cache did not operate: calls=%d statistics=%+v", calls.Load(), c.Statistics())
	}
	if err := c.InvalidateTags(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	_, err = GetTagged(context.Background(), c, "probe", time.Second, []string{"probe"}, load)
	if err != nil || calls.Load() != 2 {
		t.Fatal("live invalidation failed")
	}
	// Remove this test's tag generation; values expire within a second.
	if err := c.client.Del(context.Background(), c.tagKeys([]string{"probe"})...).Err(); err != nil {
		t.Fatal("live cleanup failed")
	}
}
