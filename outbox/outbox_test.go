package outbox

import (
	"hash/fnv"
	"testing"
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
