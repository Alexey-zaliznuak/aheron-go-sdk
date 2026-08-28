package outbox

import (
	"errors"
	"fmt"
	"hash/fnv"
	"testing"
	"time"
)

// referenceBucket is an independent restatement of the bucket function. The
// relay's hash is baked into primary keys of every outbox table ever created,
// so this test exists to make a change to it loud rather than silent.
func referenceBucket(key string, count int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(count))
}

func TestBucketOfMatchesReference(t *testing.T) {
	keys := []string{
		"", "a", "subject-1", "subject-2",
		"6f1a1f4e-1d3a-4b5e-9c2f-7a8b9c0d1e2f",
		"project:1|subject:2",
	}
	for _, key := range keys {
		if got, want := BucketOf(key, DefaultBucketCount), referenceBucket(key, DefaultBucketCount); got != want {
			t.Fatalf("BucketOf(%q) = %d, want %d", key, got, want)
		}
	}
}

func TestBucketOfIsStable(t *testing.T) {
	// Golden values. If these change, already-written rows become unreachable:
	// the relay would poll buckets the rows are not in.
	golden := map[string]int{
		"":          5, // the bare FNV-1a offset basis, 2166136261 % 16
		"a":         12,
		"subject-1": 5,
		"subject-2": 12,
	}
	for key, want := range golden {
		if got := BucketOf(key, DefaultBucketCount); got != want {
			t.Errorf("BucketOf(%q, %d) = %d, want %d", key, DefaultBucketCount, got, want)
		}
	}
}

func TestBucketOfStaysInRange(t *testing.T) {
	for _, count := range []int{1, 4, 16, 64} {
		for i := 0; i < 1000; i++ {
			key := string(rune('a'+i%26)) + string(rune('0'+i%10))
			b := BucketOf(key, count)
			if b < 0 || b >= count {
				t.Fatalf("BucketOf(%q, %d) = %d, out of range", key, count, b)
			}
		}
	}
}

func TestBucketOfDefaultsCount(t *testing.T) {
	for _, count := range []int{0, -1} {
		if got, want := BucketOf("key", count), BucketOf("key", DefaultBucketCount); got != want {
			t.Fatalf("BucketOf(%q, %d) = %d, want %d", "key", count, got, want)
		}
	}
}

func TestPublishErrorClassificationSurvivesWrapping(t *testing.T) {
	cause := errors.New("broker unavailable")

	transient := fmt.Errorf("write batch: %w", Transient(cause, 3*time.Second))
	if got := ClassifyPublishError(transient); got != PublishErrorTransient {
		t.Fatalf("transient class = %q, want %q", got, PublishErrorTransient)
	}
	if got := PublishRetryAfter(transient); got != 3*time.Second {
		t.Fatalf("retry after = %v, want 3s", got)
	}
	if !errors.Is(transient, cause) {
		t.Fatal("transient wrapper lost its cause")
	}
	if IsTerminalPublishError(transient) {
		t.Fatal("transient error must not be terminal")
	}

	permanent := fmt.Errorf("publish message: %w", Permanent(cause))
	if got := ClassifyPublishError(permanent); got != PublishErrorPermanent {
		t.Fatalf("permanent class = %q, want %q", got, PublishErrorPermanent)
	}
	if !errors.Is(permanent, cause) {
		t.Fatal("permanent wrapper lost its cause")
	}
	if IsTerminalPublishError(permanent) {
		t.Fatal("publisher-level permanent error is not terminal until Store confirms dead-lettering")
	}

	dead := fmt.Errorf("store: %w", &DeadLetteredPublishError{EventID: "event-1", Err: cause})
	if got := ClassifyPublishError(dead); got != PublishErrorDeadLettered {
		t.Fatalf("dead-lettered class = %q, want %q", got, PublishErrorDeadLettered)
	}
	if !IsTerminalPublishError(dead) {
		t.Fatal("dead-lettered error must be terminal")
	}
}

func TestPublishErrorHelpersPreserveNil(t *testing.T) {
	if err := Permanent(nil); err != nil {
		t.Fatalf("Permanent(nil) = %v, want nil", err)
	}
	if err := Transient(nil, time.Second); err != nil {
		t.Fatalf("Transient(nil) = %v, want nil", err)
	}
	if class := ClassifyPublishError(nil); class != "" {
		t.Fatalf("nil error class = %q, want empty", class)
	}
}
