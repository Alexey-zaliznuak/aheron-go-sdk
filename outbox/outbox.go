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
	"errors"
	"fmt"
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

// PublishErrorClass is the operational class of a publisher failure. The
// class is deliberately independent of any broker library: an adapter marks
// errors at its boundary, while the relay and Store can apply the same retry
// semantics for Kafka, NATS, HTTP or another transport.
type PublishErrorClass string

const (
	// PublishErrorUnknown is an unclassified publisher failure. Stores may
	// retain their legacy attempt-budget behaviour for this class, so broker
	// adapters should explicitly mark shared outages as transient.
	PublishErrorUnknown PublishErrorClass = "unknown"

	// PublishErrorTransient is a retryable transport or dependency outage. A
	// transient failure must not consume an event's poison-message attempt
	// budget.
	PublishErrorTransient PublishErrorClass = "transient"

	// PublishErrorPermanent is an event-specific rejection that cannot succeed
	// with the same payload. A Store may move that event straight to its dead
	// letter table so it does not block the rest of the bucket.
	PublishErrorPermanent PublishErrorClass = "permanent"

	// PublishErrorDeadLettered reports that the Store has already parked an
	// unclassified event after it exhausted the configured attempt budget.
	PublishErrorDeadLettered PublishErrorClass = "dead_lettered"
)

// PermanentPublishError marks an event-specific publisher failure that cannot
// succeed when retried with the same payload. Stores can safely dead-letter the
// one event immediately. Do not use it for a broker-wide authentication or
// availability incident: those are transient from the event's point of view.
type PermanentPublishError struct {
	Err error
}

func (e *PermanentPublishError) Error() string {
	if e == nil {
		return "permanent publish error"
	}
	if e.Err == nil {
		return "permanent publish error"
	}
	return e.Err.Error()
}

// Unwrap preserves errors.Is/errors.As for the original publisher error.
func (e *PermanentPublishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Permanent marks err as an event-specific permanent publish failure. A nil
// error stays nil so it is convenient to use at a return site.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentPublishError{Err: err}
}

// TransientPublishError marks a shared or otherwise retryable publisher
// failure. RetryAfter is optional; when positive it takes precedence over the
// relay's local exponential full-jitter backoff.
type TransientPublishError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *TransientPublishError) Error() string {
	if e == nil {
		return "transient publish error"
	}
	if e.Err == nil {
		return "transient publish error"
	}
	return e.Err.Error()
}

// Unwrap preserves errors.Is/errors.As for the original publisher error.
func (e *TransientPublishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Transient marks err as retryable without consuming an event's poison-message
// attempt budget. retryAfter may be zero when the dependency supplied no hint.
func Transient(err error, retryAfter time.Duration) error {
	if err == nil {
		return nil
	}
	return &TransientPublishError{Err: err, RetryAfter: retryAfter}
}

// DeadLetteredPublishError is returned by a Store after it has atomically
// moved an exhausted event out of the pending queue. The relay treats it as a
// terminal result for that row and does not open a retry backoff for the
// bucket, allowing the row behind it to proceed on the next drain tick.
type DeadLetteredPublishError struct {
	EventID string
	Err     error
}

func (e *DeadLetteredPublishError) Error() string {
	if e == nil {
		return "outbox event moved to dead letters"
	}
	if e.Err == nil {
		return fmt.Sprintf("outbox event %s moved to dead letters", e.EventID)
	}
	return fmt.Sprintf("outbox event %s moved to dead letters: %v", e.EventID, e.Err)
}

// Unwrap preserves errors.Is/errors.As for the original publisher error.
func (e *DeadLetteredPublishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ClassifyPublishError returns the class carried by err. Wrapping with %w does
// not lose the class. Untyped errors are intentionally reported as unknown so
// existing Stores can keep a bounded compatibility path while new broker
// adapters explicitly distinguish shared transient outages from poison rows.
func ClassifyPublishError(err error) PublishErrorClass {
	if err == nil {
		return ""
	}
	var dead *DeadLetteredPublishError
	if errors.As(err, &dead) {
		return PublishErrorDeadLettered
	}
	var permanent *PermanentPublishError
	if errors.As(err, &permanent) {
		return PublishErrorPermanent
	}
	var transient *TransientPublishError
	if errors.As(err, &transient) {
		return PublishErrorTransient
	}
	return PublishErrorUnknown
}

// PublishRetryAfter returns the dependency-supplied retry hint, if err carries
// one. A negative hint is treated as absent.
func PublishRetryAfter(err error) time.Duration {
	var transient *TransientPublishError
	if !errors.As(err, &transient) || transient == nil || transient.RetryAfter <= 0 {
		return 0
	}
	return transient.RetryAfter
}

// IsTerminalPublishError reports whether the Store explicitly confirmed that
// it removed the pending row. A PermanentPublishError by itself is only a
// publisher classification: a custom Store may have failed before parking the
// row, so only DeadLetteredPublishError is terminal to Relay.
func IsTerminalPublishError(err error) bool {
	return ClassifyPublishError(err) == PublishErrorDeadLettered
}

// PublishObservation is emitted once for every call to Publisher.Publish. It
// intentionally excludes event ID, payload and partition key so metric
// adapters do not accidentally create high-cardinality or customer-data
// labels. Event IDs remain present in structured relay logs.
type PublishObservation struct {
	Bucket     int
	Topic      string
	Duration   time.Duration
	ErrorClass PublishErrorClass
	Succeeded  bool
}

// BackoffObservation reports a bucket-level retry delay after a non-terminal
// Store or publisher failure.
type BackoffObservation struct {
	Bucket              int
	Delay               time.Duration
	ConsecutiveFailures int
	ErrorClass          PublishErrorClass
}

// Observer is the dependency-free metrics seam exposed by Relay. Callbacks run
// on one bounded asynchronous worker, never on the publish path. A blocked
// observer causes later observations to be dropped, and a panic is isolated;
// both counters are exposed on Relay. A Prometheus/OpenTelemetry adapter can
// turn observations into publish counters, failure counters by class, latency
// histograms and backoff gauges.
type Observer interface {
	ObservePublish(PublishObservation)
	ObserveBackoff(BackoffObservation)
}

// DeadLetterRef is the stable primary-key reference of one dead-letter row.
// A targeted replay requires all three fields, avoiding an unbounded scan by
// event ID and making the operator's target explicit.
type DeadLetterRef struct {
	Bucket    int
	CreatedAt time.Time
	ID        string
}

// DeadLetter contains the original event and its terminal failure metadata.
type DeadLetter struct {
	Event
	Attempts  int
	LastError string
	FailedAt  time.Time
}

// Ref returns the stable key that can be passed to GetDeadLetter or
// ReplayDeadLetter.
func (d DeadLetter) Ref() DeadLetterRef {
	return DeadLetterRef{Bucket: d.Bucket, CreatedAt: d.CreatedAt, ID: d.ID}
}

// DeadLetterListOptions controls keyset pagination in primary-key order. Limit
// defaults to 100 and concrete Stores may enforce a defensive maximum.
type DeadLetterListOptions struct {
	After *DeadLetterRef
	Limit int
}

// DeadLetterPage is one keyset page. Next is nil on the last page; otherwise
// pass it back as DeadLetterListOptions.After.
type DeadLetterPage struct {
	Items []DeadLetter
	Next  *DeadLetterRef
}

// ReplayState makes targeted replay idempotency visible to operator tooling.
type ReplayState string

const (
	ReplayRequeued ReplayState = "requeued"
	// ReplayAlreadyPending is returned only while the exact primary key still
	// exists in pending. Once it publishes and is deleted, a repeated replay
	// returns ErrDeadLetterNotFound rather than creating a duplicate.
	ReplayAlreadyPending ReplayState = "already_pending"
)

// OutboxStats is a pull-based, deliberately explicit snapshot for readiness,
// dashboards and alert collectors. Oldest timestamps are nil for empty tables;
// callers calculate age against their own clock.
type OutboxStats struct {
	PendingCount    uint64
	PendingOldestAt *time.Time
	DeadCount       uint64
	DeadOldestAt    *time.Time
}

// DeadLetterStore is the optional operator interface implemented by Stores
// that support inspection and targeted replay. It stays separate from Store so
// existing PostgreSQL implementations of the hot relay path do not break.
type DeadLetterStore interface {
	ListDeadLetters(ctx context.Context, opts DeadLetterListOptions) (DeadLetterPage, error)
	GetDeadLetter(ctx context.Context, ref DeadLetterRef) (DeadLetter, error)
	ReplayDeadLetter(ctx context.Context, ref DeadLetterRef) (ReplayState, error)
	OutboxStats(ctx context.Context) (OutboxStats, error)
}

// ErrDeadLetterNotFound means neither the dead-letter table nor, for an
// idempotent replay, the pending table contains the requested primary key.
type ErrDeadLetterNotFound struct {
	Ref DeadLetterRef
}

func (e ErrDeadLetterNotFound) Error() string {
	return fmt.Sprintf("outbox dead letter %s not found", e.Ref.ID)
}

// ErrDeadLetterReplayConflict protects an existing pending event from being
// overwritten if storage corruption or a manual operation left the same key in
// both pending and dead-letter tables.
type ErrDeadLetterReplayConflict struct {
	Ref DeadLetterRef
}

func (e ErrDeadLetterReplayConflict) Error() string {
	return fmt.Sprintf("outbox dead letter %s also exists in pending", e.Ref.ID)
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
	//
	// Stores with an attempt budget should not charge TransientPublishError to
	// that budget. After atomically parking a PermanentPublishError or exhausted
	// row, they must return DeadLetteredPublishError; publisher classification
	// alone does not prove that pending storage was changed. An untyped error is
	// deliberately left to the Store's legacy policy for backward compatibility.
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
