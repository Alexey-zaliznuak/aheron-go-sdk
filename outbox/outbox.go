// Package outbox runs the relay half of the transactional-outbox pattern: rows
// written in the same transaction as the change they describe are read back out
// and published to a message broker.
//
// The package is storage-agnostic — it never touches a database. A Store
// supplies the two operations the relay needs, so any database can back it. A
// ready-made YDB Store lives in github.com/Alexey-zaliznuak/aheron-go-sdk/ydb;
// on PostgreSQL the same two methods are a short piece of SQL over
// SELECT ... FOR UPDATE SKIP LOCKED.
//
// The queue is sharded into buckets, and an instance publishes only the buckets
// it holds a lease on. Instances therefore divide the queue between them
// instead of racing for individual rows, which is what makes the pattern work
// on a database without row-level skip-locked reads. A single instance simply
// takes every bucket.
package outbox

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"
)

// Logger is the structured logging seam the relay writes to. It is the same
// interface the rest of the SDK uses, so integration.Logger and a zaplog
// adapter satisfy it as-is. The default is a no-op.
type Logger = logx.Logger

// LogField is a single structured key/value on a log record.
type LogField = logx.Field

// Event is one row of the outbox.
type Event struct {
	// ID identifies the row. It appears in relay logs, and a Store uses it to
	// address the row once publishing has succeeded.
	ID string

	// Bucket is the shard of the queue this row belongs to, derived from
	// PartitionKey with BucketOf.
	Bucket int

	CreatedAt time.Time

	// PartitionKey decides both the bucket and the broker partition. That is
	// what makes per-key ordering hold end to end: rows sharing a key sit in
	// one bucket, are published sequentially, and land in one broker partition.
	PartitionKey string

	Payload []byte

	// Topic optionally overrides the publisher's default destination, so one
	// relay can fan rows out to several topics. Empty means the default.
	Topic string
}

// Store is the database behind the relay.
//
// Implementations own the whole lease protocol; the relay only calls these two
// methods on a timer. Both must be safe to call concurrently for different
// buckets.
type Store interface {
	// ClaimBuckets takes every bucket that is free or whose lease has expired,
	// renews the ones this owner already holds, and returns the full set the
	// owner holds afterwards. It is called once at start-up and then on a
	// timer at a third of lease.
	ClaimBuckets(ctx context.Context, owner string, lease time.Duration) ([]int, error)

	// PublishBucket reads up to limit of the bucket's oldest rows and calls
	// publish for each of them in key order, discarding a row once its publish
	// has returned nil. It returns how many rows it published.
	//
	// Two obligations make the difference between an outbox and a queue that
	// loses or reorders messages. Publishing must stop at the first failure
	// rather than skipping ahead, because the rows behind it share a partition
	// key and would arrive out of order. And the owner must be re-checked
	// against the lease, so a bucket that changed hands mid-drain publishes
	// nothing: returning (0, nil) is the correct answer there.
	PublishBucket(ctx context.Context, bucket int, owner string, limit int,
		publish func(context.Context, Event) error) (published int, err error)
}

// Publisher delivers one event to the broker.
type Publisher interface {
	Publish(ctx context.Context, ev Event) error
}

// PublisherFunc adapts a plain function to Publisher.
type PublisherFunc func(ctx context.Context, ev Event) error

// Publish implements Publisher.
func (f PublisherFunc) Publish(ctx context.Context, ev Event) error { return f(ctx, ev) }

// DefaultBucketCount is how many buckets a queue is sharded into unless the
// schema says otherwise. Sixteen is the value CockroachDB picked as the default
// for its hash-sharded indexes, on the same reasoning: enough to spread writes
// across shards, few enough that one instance can hold them all.
const DefaultBucketCount = 16

// BucketOf maps a partition key onto its bucket.
//
// This function must stay stable forever. The bucket is part of the outbox
// primary key, so changing the hash would strand already-written rows in
// buckets nobody polls. count has to match the value the table was designed
// with; a zero or negative count means DefaultBucketCount.
func BucketOf(partitionKey string, count int) int {
	if count <= 0 {
		count = DefaultBucketCount
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(partitionKey))
	return int(h.Sum32() % uint32(count))
}
