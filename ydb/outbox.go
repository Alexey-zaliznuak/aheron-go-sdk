package ydb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/outbox"

	"github.com/google/uuid"
	ydbsdk "github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/query"
)

// DefaultOutboxTable is the outbox table name assumed when OutboxConfig leaves
// it empty. The lease and dead-letter tables derive from it.
const DefaultOutboxTable = "platform_outbox"

// DefaultMaxAttempts is how many unclassified, event-specific publisher
// failures a row gets before it is parked in the dead-letter table. Explicit
// transient failures never consume this budget; explicit permanent failures
// are parked immediately.
const DefaultMaxAttempts = 10

const (
	defaultDeadLetterPageSize = 100
	maxDeadLetterPageSize     = 1000
)

var errOutboxLeaseLost = errors.New("ydb: outbox bucket lease lost")

// OutboxConfig describes where a service's outbox lives.
type OutboxConfig struct {
	// TablePathPrefix is the absolute path of the service's YDB directory.
	// Leave it empty when the connection string already carries
	// table_path_prefix.
	TablePathPrefix string

	// Table is the outbox table. The lease and dead-letter tables are this
	// name with "_leases" and "_dead" appended. Empty means DefaultOutboxTable.
	Table string

	// BucketCount must match the number of buckets the table was designed
	// with. It cannot be changed later: the bucket is part of the primary key,
	// so a different count would strand written rows in buckets nobody polls.
	// Zero means outbox.DefaultBucketCount.
	BucketCount int

	// MaxAttempts caps unclassified publish failures before a row moves to the
	// dead-letter table. Zero means DefaultMaxAttempts. A publisher must wrap a
	// broker-wide outage with outbox.Transient so it updates last_error without
	// consuming this poison-message budget. outbox.Permanent moves an
	// event-specific rejection to dead letters immediately.
	//
	// Parking a poison row is not optional here the way it was on PostgreSQL.
	// There, a status column with a partial index hid the row from the relay's
	// scan; YDB has no partial index, so a row left in place is re-read on
	// every poll and blocks its whole bucket forever.
	MaxAttempts int
}

// OutboxStore implements outbox.Store on the standard outbox schema. The DDL it
// expects is in this package's README.
type OutboxStore struct {
	db     *ydbsdk.Driver
	pragma string

	table  string
	leases string
	dead   string

	bucketCount int
	maxAttempts int
}

var _ outbox.DeadLetterStore = (*OutboxStore)(nil)

// NewOutboxStore builds a store over an already-open driver.
func NewOutboxStore(db *ydbsdk.Driver, cfg OutboxConfig) *OutboxStore {
	if cfg.Table == "" {
		cfg.Table = DefaultOutboxTable
	}
	if cfg.BucketCount <= 0 {
		cfg.BucketCount = outbox.DefaultBucketCount
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}

	return &OutboxStore{
		db:          db,
		pragma:      pathPrefix(cfg.TablePathPrefix),
		table:       cfg.Table,
		leases:      cfg.Table + "_leases",
		dead:        cfg.Table + "_dead",
		bucketCount: cfg.BucketCount,
		maxAttempts: cfg.MaxAttempts,
	}
}

// BucketCount reports the sharding the store was built with.
func (s *OutboxStore) BucketCount() int { return s.bucketCount }

func (s *OutboxStore) q(format string, args ...any) string {
	return s.pragma + fmt.Sprintf(format, args...)
}

// EnqueueTx writes one event inside the caller's transaction.
//
// The transaction is the entire point: the row must be committed together with
// the change it describes, or the outbox guarantees nothing. The returned event
// carries the identifier, bucket and timestamp the row was written with.
//
// The bucket is always derived from the partition key here rather than taken
// from the caller, so every writer shards the queue the same way.
func (s *OutboxStore) EnqueueTx(ctx context.Context, tx query.TxActor, ev outbox.Event) (outbox.Event, error) {
	if ev.PartitionKey == "" {
		return outbox.Event{}, fmt.Errorf("ydb: outbox event needs a partition key")
	}

	id := uuid.New()
	if ev.ID != "" {
		parsed, err := uuid.Parse(ev.ID)
		if err != nil {
			return outbox.Event{}, fmt.Errorf("ydb: outbox event id: %w", err)
		}
		id = parsed
	}
	ev.ID = id.String()
	ev.Bucket = outbox.BucketOf(ev.PartitionKey, s.bucketCount)
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = Now()
	} else {
		ev.CreatedAt = ev.CreatedAt.UTC().Truncate(time.Microsecond)
	}
	if len(ev.Payload) == 0 {
		ev.Payload = []byte("{}")
	}

	err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;
DECLARE $partition_key AS Utf8;
DECLARE $topic AS Optional<Utf8>;
DECLARE $payload AS Json;

INSERT INTO %s (bucket, created_at, id, partition_key, topic, payload, attempts)
VALUES ($bucket, $created_at, $id, $partition_key, $topic, $payload, 0);`, s.table),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(ev.Bucket)).
			Param("$created_at").Timestamp(ev.CreatedAt).
			Param("$id").Uuid(id).
			Param("$partition_key").Text(ev.PartitionKey).
			Param("$topic").BeginOptional().Text(optionalText(ev.Topic)).EndOptional().
			Param("$payload").JSON(string(ev.Payload)).
			Build()))
	if err != nil {
		return outbox.Event{}, fmt.Errorf("ydb: enqueue outbox event: %w", err)
	}
	return ev, nil
}

// ClaimBuckets takes every bucket that is free or whose lease has expired, and
// renews the ones this owner already holds.
//
// This is what replaces SELECT ... FOR UPDATE SKIP LOCKED, which YDB does not
// have. Relay instances no longer race for individual rows; they divide the
// buckets between them and each publishes its own in key order. A dead
// instance's buckets return to circulation when its lease lapses.
func (s *OutboxStore) ClaimBuckets(ctx context.Context, owner string, lease time.Duration) ([]int, error) {
	var owned []int
	err := s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		owned = nil

		rs, err := tx.QueryResultSet(ctx, s.q(`
SELECT bucket, locked_by, locked_until FROM %s;`, s.leases))
		if err != nil {
			return err
		}

		type held struct {
			by    string
			until time.Time
		}
		leases := map[uint8]held{}
		if _, err := collect(ctx, rs, func(row query.Row) (struct{}, error) {
			var (
				bucket uint8
				h      held
			)
			if err := row.Scan(&bucket, &h.by, &h.until); err != nil {
				return struct{}{}, err
			}
			leases[bucket] = h
			return struct{}{}, nil
		}); err != nil {
			return err
		}

		stamp := Now()
		lockedUntil := stamp.Add(lease).Truncate(time.Microsecond)
		for bucket := 0; bucket < s.bucketCount; bucket++ {
			current, taken := leases[uint8(bucket)]
			if taken && current.by != owner && current.until.After(stamp) {
				continue
			}

			if err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $locked_by AS Utf8;
DECLARE $locked_until AS Timestamp;

UPSERT INTO %s (bucket, locked_by, locked_until)
VALUES ($bucket, $locked_by, $locked_until);`, s.leases),
				query.WithParameters(ydbsdk.ParamsBuilder().
					Param("$bucket").Uint8(uint8(bucket)).
					Param("$locked_by").Text(owner).
					Param("$locked_until").Timestamp(lockedUntil).
					Build())); err != nil {
				return err
			}
			owned = append(owned, bucket)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ydb: claim outbox buckets: %w", err)
	}
	return owned, nil
}

// PublishBucket publishes one bucket's oldest rows, in key order.
//
// The publish call stays outside any transaction: it is a network write to a
// broker, and holding a transaction across it would pin a session for its
// duration and make a transaction retry republish messages. Each row is instead
// deleted in its own short transaction right after its publish returns, which
// leaves a crash window of a single in-flight message — the same window the
// PostgreSQL relay had, and one an idempotent consumer absorbs.
func (s *OutboxStore) PublishBucket(
	ctx context.Context,
	bucket int,
	owner string,
	limit int,
	publish func(context.Context, outbox.Event) error,
) (int, error) {
	if limit <= 0 {
		limit = 100
	}

	events, err := s.leasedBucketEvents(ctx, bucket, owner, limit)
	if err != nil {
		return 0, err
	}

	published := 0
	for _, event := range events {
		held, err := s.leaseHeld(ctx, event.Bucket, owner)
		if err != nil {
			return published, err
		}
		if !held {
			return published, outbox.Transient(
				fmt.Errorf("%w: bucket %d owner %s", errOutboxLeaseLost, event.Bucket, owner), 0)
		}

		if publishErr := publish(ctx, event); publishErr != nil {
			deadLettered, err := s.recordPublishFailure(ctx, event, owner, publishErr)
			if err != nil {
				if errors.Is(err, errOutboxLeaseLost) {
					return published, outbox.Transient(err, 0)
				}
				return published, err
			}
			if deadLettered {
				publishErr = &outbox.DeadLetteredPublishError{EventID: event.ID, Err: publishErr}
			}
			// Stop at the first failure. The rows behind it are in the same
			// bucket and may share a partition key, so skipping ahead would
			// deliver them out of order.
			return published, fmt.Errorf("ydb: publish outbox event %s: %w", event.ID, publishErr)
		}

		if err := s.deleteEvent(ctx, event, owner); err != nil {
			if errors.Is(err, errOutboxLeaseLost) {
				return published, outbox.Transient(err, 0)
			}
			return published, err
		}
		published++
	}
	return published, nil
}

// leasedBucketEvents returns the bucket's oldest rows, or nothing when the
// lease is no longer ours. The lease check and the read share a transaction so
// a bucket cannot change hands between them.
func (s *OutboxStore) leasedBucketEvents(ctx context.Context, bucket int, owner string, limit int) ([]outbox.Event, error) {
	var events []outbox.Event
	err := s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		events = nil

		row, err := tx.QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;

SELECT locked_by, locked_until FROM %s WHERE bucket = $bucket;`, s.leases),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(bucket)).
				Build()))
		if err != nil {
			if IsNotFound(err) {
				return nil
			}
			return err
		}

		var (
			lockedBy    string
			lockedUntil time.Time
		)
		if err := row.Scan(&lockedBy, &lockedUntil); err != nil {
			return err
		}
		if lockedBy != owner || !lockedUntil.After(Now()) {
			return nil
		}

		rs, err := tx.QueryResultSet(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $limit AS Uint64;

SELECT bucket, created_at, id, partition_key, topic, payload
FROM %s
WHERE bucket = $bucket
ORDER BY bucket, created_at, id
LIMIT $limit;`, s.table),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(bucket)).
				Param("$limit").Uint64(uint64(limit)).
				Build()))
		if err != nil {
			return err
		}

		events, err = collect(ctx, rs, scanEvent)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ydb: read outbox bucket %d: %w", bucket, err)
	}
	return events, nil
}

func (s *OutboxStore) leaseHeld(ctx context.Context, bucket int, owner string) (bool, error) {
	row, err := s.db.Query().QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;

SELECT locked_by, locked_until FROM %s WHERE bucket = $bucket;`, s.leases),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(bucket)).
			Build()))
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("ydb: check outbox bucket %d lease: %w", bucket, err)
	}
	var (
		lockedBy    string
		lockedUntil time.Time
	)
	if err := row.Scan(&lockedBy, &lockedUntil); err != nil {
		return false, fmt.Errorf("ydb: scan outbox bucket %d lease: %w", bucket, err)
	}
	return lockedBy == owner && lockedUntil.After(Now()), nil
}

func (s *OutboxStore) leaseHeldTx(
	ctx context.Context,
	tx query.TxActor,
	bucket int,
	owner string,
) (bool, error) {
	row, err := tx.QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;

SELECT locked_by, locked_until FROM %s WHERE bucket = $bucket;`, s.leases),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(bucket)).
			Build()))
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	var (
		lockedBy    string
		lockedUntil time.Time
	)
	if err := row.Scan(&lockedBy, &lockedUntil); err != nil {
		return false, err
	}
	return lockedBy == owner && lockedUntil.After(Now()), nil
}

func scanEvent(row query.Row) (outbox.Event, error) {
	var (
		event      outbox.Event
		bucketByte uint8
		id         uuid.UUID
		topic      *string
		payload    []byte
	)
	if err := row.Scan(&bucketByte, &event.CreatedAt, &id,
		&event.PartitionKey, &topic, &payload); err != nil {
		return outbox.Event{}, err
	}
	event.Bucket = int(bucketByte)
	event.ID = id.String()
	event.Payload = payload
	if topic != nil {
		event.Topic = *topic
	}
	return event, nil
}

func (s *OutboxStore) deleteEvent(ctx context.Context, event outbox.Event, owner string) error {
	id, err := uuid.Parse(event.ID)
	if err != nil {
		return fmt.Errorf("ydb: outbox event id: %w", err)
	}

	err = s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		held, err := s.leaseHeldTx(ctx, tx, event.Bucket, owner)
		if err != nil {
			return err
		}
		if !held {
			return errOutboxLeaseLost
		}
		return tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

DELETE FROM %s WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(event.Bucket)).
				Param("$created_at").Timestamp(event.CreatedAt).
				Param("$id").Uuid(id).
				Build()))
	})
	if err != nil {
		return fmt.Errorf("ydb: delete outbox event %s: %w", event.ID, err)
	}
	return nil
}

// recordPublishFailure applies the failure class atomically. Shared transient
// outages update last_error without consuming MaxAttempts. Permanent failures
// move directly to dead letters. Untyped failures keep the legacy bounded
// budget so existing publishers cannot block a bucket forever.
func (s *OutboxStore) recordPublishFailure(
	ctx context.Context,
	event outbox.Event,
	owner string,
	publishErr error,
) (bool, error) {
	id, err := uuid.Parse(event.ID)
	if err != nil {
		return false, fmt.Errorf("ydb: outbox event id: %w", err)
	}

	deadLettered := false
	err = s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		deadLettered = false
		held, err := s.leaseHeldTx(ctx, tx, event.Bucket, owner)
		if err != nil {
			return err
		}
		if !held {
			return errOutboxLeaseLost
		}
		row, err := tx.QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

SELECT partition_key, topic, payload, attempts
FROM %s
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(event.Bucket)).
				Param("$created_at").Timestamp(event.CreatedAt).
				Param("$id").Uuid(id).
				Build()))
		if err != nil {
			// The row is gone: another relay published it. Nothing to record.
			if IsNotFound(err) {
				return nil
			}
			return err
		}

		var (
			partitionKey string
			topic        *string
			payload      []byte
			attempts     int32
		)
		if err := row.Scan(&partitionKey, &topic, &payload, &attempts); err != nil {
			return err
		}

		class := outbox.ClassifyPublishError(publishErr)
		lastError := strings.ToValidUTF8(publishErr.Error(), "")

		if class == outbox.PublishErrorTransient {
			return tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;
DECLARE $last_error AS Utf8;

UPDATE %s SET last_error = $last_error
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
				query.WithParameters(ydbsdk.ParamsBuilder().
					Param("$bucket").Uint8(uint8(event.Bucket)).
					Param("$created_at").Timestamp(event.CreatedAt).
					Param("$id").Uuid(id).
					Param("$last_error").Text(lastError).
					Build()))
		}

		attempts++
		if class != outbox.PublishErrorPermanent && int(attempts) < s.maxAttempts {
			return tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;
DECLARE $attempts AS Int32;
DECLARE $last_error AS Utf8;

UPDATE %s SET attempts = $attempts, last_error = $last_error
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
				query.WithParameters(ydbsdk.ParamsBuilder().
					Param("$bucket").Uint8(uint8(event.Bucket)).
					Param("$created_at").Timestamp(event.CreatedAt).
					Param("$id").Uuid(id).
					Param("$attempts").Int32(attempts).
					Param("$last_error").Text(lastError).
					Build()))
		}

		if err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;
DECLARE $partition_key AS Utf8;
DECLARE $topic AS Optional<Utf8>;
DECLARE $payload AS Json;
DECLARE $attempts AS Int32;
DECLARE $last_error AS Utf8;
DECLARE $failed_at AS Timestamp;

UPSERT INTO %s (
    bucket, created_at, id, partition_key, topic, payload,
    attempts, last_error, failed_at
) VALUES (
    $bucket, $created_at, $id, $partition_key, $topic, $payload,
    $attempts, $last_error, $failed_at
);`, s.dead),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(event.Bucket)).
				Param("$created_at").Timestamp(event.CreatedAt).
				Param("$id").Uuid(id).
				Param("$partition_key").Text(partitionKey).
				Param("$topic").BeginOptional().Text(topic).EndOptional().
				Param("$payload").JSON(string(payload)).
				Param("$attempts").Int32(attempts).
				Param("$last_error").Text(lastError).
				Param("$failed_at").Timestamp(Now()).
				Build())); err != nil {
			return err
		}

		if err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

DELETE FROM %s WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(event.Bucket)).
				Param("$created_at").Timestamp(event.CreatedAt).
				Param("$id").Uuid(id).
				Build())); err != nil {
			return err
		}
		deadLettered = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("ydb: record outbox failure for %s: %w", event.ID, err)
	}
	return deadLettered, nil
}

// ListDeadLetters returns dead rows in stable primary-key order. It is an
// operator path, not part of the relay hot loop; no extra index or schema
// column is required.
func (s *OutboxStore) ListDeadLetters(ctx context.Context, opts outbox.DeadLetterListOptions) (outbox.DeadLetterPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultDeadLetterPageSize
	}
	if limit > maxDeadLetterPageSize {
		limit = maxDeadLetterPageSize
	}

	var (
		rs  query.ResultSet
		err error
	)
	if opts.After == nil {
		rs, err = s.db.Query().QueryResultSet(ctx, s.q(`
DECLARE $limit AS Uint64;

SELECT bucket, created_at, id, partition_key, topic, payload,
       attempts, last_error, failed_at
FROM %s
ORDER BY bucket, created_at, id
LIMIT $limit;`, s.dead),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$limit").Uint64(uint64(limit+1)).
				Build()))
	} else {
		id, createdAt, validateErr := s.validateDeadLetterRef(*opts.After)
		if validateErr != nil {
			return outbox.DeadLetterPage{}, validateErr
		}
		rs, err = s.db.Query().QueryResultSet(ctx, s.q(`
DECLARE $after_bucket AS Uint8;
DECLARE $after_created_at AS Timestamp;
DECLARE $after_id AS Uuid;
DECLARE $limit AS Uint64;

SELECT bucket, created_at, id, partition_key, topic, payload,
       attempts, last_error, failed_at
FROM %s
WHERE (bucket, created_at, id) > ($after_bucket, $after_created_at, $after_id)
ORDER BY bucket, created_at, id
LIMIT $limit;`, s.dead),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$after_bucket").Uint8(uint8(opts.After.Bucket)).
				Param("$after_created_at").Timestamp(createdAt).
				Param("$after_id").Uuid(id).
				Param("$limit").Uint64(uint64(limit+1)).
				Build()))
	}
	if err != nil {
		return outbox.DeadLetterPage{}, fmt.Errorf("ydb: list outbox dead letters: %w", err)
	}

	items, err := collect(ctx, rs, scanDeadLetter)
	if err != nil {
		return outbox.DeadLetterPage{}, fmt.Errorf("ydb: scan outbox dead letters: %w", err)
	}

	page := outbox.DeadLetterPage{Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := page.Items[len(page.Items)-1].Ref()
		page.Next = &next
	}
	return page, nil
}

// GetDeadLetter reads one exact dead row without scanning by UUID.
func (s *OutboxStore) GetDeadLetter(ctx context.Context, ref outbox.DeadLetterRef) (outbox.DeadLetter, error) {
	id, createdAt, err := s.validateDeadLetterRef(ref)
	if err != nil {
		return outbox.DeadLetter{}, err
	}

	row, err := s.db.Query().QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

SELECT bucket, created_at, id, partition_key, topic, payload,
       attempts, last_error, failed_at
FROM %s
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.dead),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(ref.Bucket)).
			Param("$created_at").Timestamp(createdAt).
			Param("$id").Uuid(id).
			Build()))
	if err != nil {
		if IsNotFound(err) {
			return outbox.DeadLetter{}, outbox.ErrDeadLetterNotFound{Ref: ref}
		}
		return outbox.DeadLetter{}, fmt.Errorf("ydb: get outbox dead letter %s: %w", ref.ID, err)
	}

	letter, err := scanDeadLetter(row)
	if err != nil {
		return outbox.DeadLetter{}, fmt.Errorf("ydb: scan outbox dead letter %s: %w", ref.ID, err)
	}
	return letter, nil
}

// ReplayDeadLetter atomically restores one exact dead row to pending with a
// clean attempt budget. Repeating while the row remains pending returns
// ReplayAlreadyPending; after delivery it returns ErrDeadLetterNotFound and
// never recreates a duplicate. If both copies exist, neither is modified.
func (s *OutboxStore) ReplayDeadLetter(ctx context.Context, ref outbox.DeadLetterRef) (outbox.ReplayState, error) {
	id, createdAt, err := s.validateDeadLetterRef(ref)
	if err != nil {
		return "", err
	}

	var state outbox.ReplayState
	err = s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		state = ""

		pending, err := s.eventExistsTx(ctx, tx, s.table, ref.Bucket, createdAt, id)
		if err != nil {
			return err
		}

		row, err := tx.QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

SELECT bucket, created_at, id, partition_key, topic, payload,
       attempts, last_error, failed_at
FROM %s
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.dead),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(ref.Bucket)).
				Param("$created_at").Timestamp(createdAt).
				Param("$id").Uuid(id).
				Build()))
		if err != nil {
			if IsNotFound(err) {
				if pending {
					state = outbox.ReplayAlreadyPending
					return nil
				}
				return outbox.ErrDeadLetterNotFound{Ref: ref}
			}
			return err
		}

		letter, err := scanDeadLetter(row)
		if err != nil {
			return err
		}
		if pending {
			return outbox.ErrDeadLetterReplayConflict{Ref: ref}
		}

		if err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;
DECLARE $partition_key AS Utf8;
DECLARE $topic AS Optional<Utf8>;
DECLARE $payload AS Json;

UPSERT INTO %s (
    bucket, created_at, id, partition_key, topic, payload, attempts
) VALUES (
    $bucket, $created_at, $id, $partition_key, $topic, $payload, 0
);`, s.table),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(letter.Bucket)).
				Param("$created_at").Timestamp(letter.CreatedAt).
				Param("$id").Uuid(id).
				Param("$partition_key").Text(letter.PartitionKey).
				Param("$topic").BeginOptional().Text(optionalText(letter.Topic)).EndOptional().
				Param("$payload").JSON(string(letter.Payload)).
				Build())); err != nil {
			return err
		}

		if err := tx.Exec(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

DELETE FROM %s WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.dead),
			query.WithParameters(ydbsdk.ParamsBuilder().
				Param("$bucket").Uint8(uint8(ref.Bucket)).
				Param("$created_at").Timestamp(createdAt).
				Param("$id").Uuid(id).
				Build())); err != nil {
			return err
		}

		state = outbox.ReplayRequeued
		return nil
	})
	if err != nil {
		var notFound outbox.ErrDeadLetterNotFound
		var conflict outbox.ErrDeadLetterReplayConflict
		switch {
		case errors.As(err, &notFound), errors.As(err, &conflict):
			return "", err
		default:
			return "", fmt.Errorf("ydb: replay outbox dead letter %s: %w", ref.ID, err)
		}
	}
	return state, nil
}

// OutboxStats returns pending/dead counts and their oldest timestamps. It is
// intended for a low-frequency metrics collector, not for every relay tick.
func (s *OutboxStore) OutboxStats(ctx context.Context) (outbox.OutboxStats, error) {
	pendingCount, pendingOldest, err := s.tableStats(ctx, s.table, "created_at")
	if err != nil {
		return outbox.OutboxStats{}, fmt.Errorf("ydb: read pending outbox stats: %w", err)
	}
	deadCount, deadOldest, err := s.tableStats(ctx, s.dead, "failed_at")
	if err != nil {
		return outbox.OutboxStats{}, fmt.Errorf("ydb: read dead outbox stats: %w", err)
	}
	return outbox.OutboxStats{
		PendingCount:    pendingCount,
		PendingOldestAt: pendingOldest,
		DeadCount:       deadCount,
		DeadOldestAt:    deadOldest,
	}, nil
}

func (s *OutboxStore) validateDeadLetterRef(ref outbox.DeadLetterRef) (uuid.UUID, time.Time, error) {
	if ref.Bucket < 0 || ref.Bucket >= s.bucketCount {
		return uuid.Nil, time.Time{}, fmt.Errorf("ydb: outbox dead letter bucket %d is outside [0,%d)", ref.Bucket, s.bucketCount)
	}
	if ref.CreatedAt.IsZero() {
		return uuid.Nil, time.Time{}, fmt.Errorf("ydb: outbox dead letter created_at is required")
	}
	id, err := uuid.Parse(ref.ID)
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("ydb: outbox dead letter id: %w", err)
	}
	return id, ref.CreatedAt.UTC().Truncate(time.Microsecond), nil
}

func (s *OutboxStore) eventExistsTx(
	ctx context.Context,
	tx query.TxActor,
	table string,
	bucket int,
	createdAt time.Time,
	id uuid.UUID,
) (bool, error) {
	row, err := tx.QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

SELECT id FROM %s
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, table),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(bucket)).
			Param("$created_at").Timestamp(createdAt).
			Param("$id").Uuid(id).
			Build()))
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	var found uuid.UUID
	if err := row.Scan(&found); err != nil {
		return false, err
	}
	return true, nil
}

func (s *OutboxStore) tableStats(ctx context.Context, table, oldestColumn string) (uint64, *time.Time, error) {
	row, err := s.db.Query().QueryRow(ctx, s.q(`
SELECT COUNT(*), MIN(%s) FROM %s;`, oldestColumn, table))
	if err != nil {
		return 0, nil, err
	}
	var (
		count  uint64
		oldest *time.Time
	)
	if err := row.Scan(&count, &oldest); err != nil {
		return 0, nil, err
	}
	return count, oldest, nil
}

func scanDeadLetter(row query.Row) (outbox.DeadLetter, error) {
	var (
		letter     outbox.DeadLetter
		bucketByte uint8
		id         uuid.UUID
		topic      *string
		payload    []byte
		attempts   int32
		lastError  *string
	)
	if err := row.Scan(&bucketByte, &letter.CreatedAt, &id,
		&letter.PartitionKey, &topic, &payload,
		&attempts, &lastError, &letter.FailedAt); err != nil {
		return outbox.DeadLetter{}, err
	}
	letter.Bucket = int(bucketByte)
	letter.ID = id.String()
	letter.Payload = payload
	letter.Attempts = int(attempts)
	if topic != nil {
		letter.Topic = *topic
	}
	if lastError != nil {
		letter.LastError = *lastError
	}
	return letter, nil
}

// optionalText renders an empty string as an absent value, so a column that
// means "unset" stores NULL rather than "".
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
