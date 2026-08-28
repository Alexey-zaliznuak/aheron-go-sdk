package ydb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/outbox"

	"github.com/google/uuid"
	ydbsdk "github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/query"
)

// These tests run against the local YDB from the infrastructure repository
// (`task ydb:up`). They are skipped when it is not reachable, so `go test ./...`
// stays green without Docker.
//
// A mock would prove nothing here: what is being checked is whether the YQL is
// accepted and whether the tables behave the way the store assumes — a lease
// that only one owner can hold, a bucket that publishes in key order, and a row
// that gives up and moves to the dead-letter table instead of blocking its
// bucket forever.
const (
	defaultTestDSN    = "grpc://127.0.0.1:2136/local"
	defaultTestPrefix = "/local"

	// connectTimeout bounds the one connection attempt the package makes. The
	// driver otherwise waits out the whole context before admitting there is no
	// server, which would cost a minute per test on a machine without Docker.
	connectTimeout = 5 * time.Second
)

var (
	testDB     *ydbsdk.Driver
	testPrefix string
	skipReason string
)

// TestMain connects once for the whole package. Every test then builds its own
// tables on that connection, so a run without a local database pays the dial
// timeout a single time instead of once per test.
func TestMain(m *testing.M) {
	dsn := os.Getenv("YDB_TEST_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	testPrefix = os.Getenv("YDB_TEST_TABLE_PATH_PREFIX")
	if testPrefix == "" {
		testPrefix = defaultTestPrefix
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	db, err := ydbsdk.Open(ctx, dsn)
	switch {
	case err != nil:
		skipReason = fmt.Sprintf("local YDB is not reachable at %s: %v", dsn, err)
	default:
		if err := db.Query().Exec(ctx, "SELECT 1;"); err != nil {
			skipReason = fmt.Sprintf("local YDB does not answer: %v", err)
		} else {
			testDB = db
		}
	}
	cancel()

	code := m.Run()

	if testDB != nil {
		_ = testDB.Close(context.Background())
	}
	os.Exit(code)
}

func testStore(t *testing.T, cfg OutboxConfig) (*OutboxStore, context.Context) {
	t.Helper()

	if testDB == nil {
		t.Skip(skipReason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	// Each test owns its own set of tables, so they never see each other's rows
	// and a failed run leaves nothing behind for the next one.
	cfg.TablePathPrefix = testPrefix
	cfg.Table = fmt.Sprintf("sdk_outbox_test_%d", time.Now().UnixNano())

	store := NewOutboxStore(testDB, cfg)
	createOutboxTables(t, ctx, store)
	return store, ctx
}

func createOutboxTables(t *testing.T, ctx context.Context, s *OutboxStore) {
	t.Helper()

	ddl := []string{
		fmt.Sprintf(`CREATE TABLE %s (
    bucket        Uint8 NOT NULL,
    created_at    Timestamp NOT NULL,
    id            Uuid NOT NULL,
    partition_key Utf8 NOT NULL,
    topic         Utf8,
    payload       Json NOT NULL,
    attempts      Int32 NOT NULL,
    last_error    Utf8,
    PRIMARY KEY (bucket, created_at, id)
);`, s.table),
		fmt.Sprintf(`CREATE TABLE %s (
    bucket       Uint8 NOT NULL,
    locked_by    Utf8 NOT NULL,
    locked_until Timestamp NOT NULL,
    PRIMARY KEY (bucket)
);`, s.leases),
		fmt.Sprintf(`CREATE TABLE %s (
    bucket        Uint8 NOT NULL,
    created_at    Timestamp NOT NULL,
    id            Uuid NOT NULL,
    partition_key Utf8 NOT NULL,
    topic         Utf8,
    payload       Json NOT NULL,
    attempts      Int32 NOT NULL,
    last_error    Utf8,
    failed_at     Timestamp NOT NULL,
    PRIMARY KEY (bucket, created_at, id)
);`, s.dead),
	}

	for _, stmt := range ddl {
		if err := s.db.Query().Exec(ctx, s.pragma+stmt); err != nil {
			t.Fatalf("create outbox table: %v", err)
		}
	}

	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, table := range []string{s.table, s.leases, s.dead} {
			_ = s.db.Query().Exec(bg, s.pragma+fmt.Sprintf("DROP TABLE %s;", table))
		}
	})
}

func enqueue(t *testing.T, ctx context.Context, s *OutboxStore, partitionKey, payload string) outbox.Event {
	t.Helper()

	var written outbox.Event
	err := s.db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
		var err error
		written, err = s.EnqueueTx(ctx, tx, outbox.Event{
			PartitionKey: partitionKey,
			Payload:      []byte(payload),
		})
		return err
	})
	if err != nil {
		t.Fatalf("enqueue %q: %v", partitionKey, err)
	}
	return written
}

func drain(t *testing.T, ctx context.Context, s *OutboxStore, bucket int, owner string, limit int) []outbox.Event {
	t.Helper()

	var got []outbox.Event
	if _, err := s.PublishBucket(ctx, bucket, owner, limit, func(_ context.Context, ev outbox.Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("publish bucket %d: %v", bucket, err)
	}
	return got
}

func TestEnqueueDerivesBucketFromPartitionKey(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})

	written := enqueue(t, ctx, store, "subject-1", `{"a":1}`)

	if want := outbox.BucketOf("subject-1", store.BucketCount()); written.Bucket != want {
		t.Fatalf("bucket = %d, want %d", written.Bucket, want)
	}
	if written.ID == "" || written.CreatedAt.IsZero() {
		t.Fatalf("enqueue returned an incomplete event: %+v", written)
	}
}

func TestPublishBucketDeliversInOrderAndClearsRows(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})
	const owner = "owner-a"

	// One partition key means one bucket, which is what makes the order
	// observable in the first place.
	first := enqueue(t, ctx, store, "subject-1", `{"n":1}`)
	enqueue(t, ctx, store, "subject-1", `{"n":2}`)
	enqueue(t, ctx, store, "subject-1", `{"n":3}`)

	if _, err := store.ClaimBuckets(ctx, owner, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	got := drain(t, ctx, store, first.Bucket, owner, 10)
	if len(got) != 3 {
		t.Fatalf("published %d events, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Fatalf("events published out of order: %v then %v", got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}

	// A published row is deleted rather than marked: there is no partial index
	// to hide a growing tail behind, so the tail would be re-read every poll.
	if again := drain(t, ctx, store, first.Bucket, owner, 10); len(again) != 0 {
		t.Fatalf("bucket still holds %d events after a successful drain", len(again))
	}
}

func TestClaimBucketsIsExclusive(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})

	first, err := store.ClaimBuckets(ctx, "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("claim by owner-a: %v", err)
	}
	if len(first) != store.BucketCount() {
		t.Fatalf("owner-a took %d buckets, want all %d", len(first), store.BucketCount())
	}

	second, err := store.ClaimBuckets(ctx, "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("claim by owner-b: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("owner-b took %v while owner-a's lease is live", second)
	}

	// Renewing must not be mistaken for a fresh claim: the holder keeps its set.
	renewed, err := store.ClaimBuckets(ctx, "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("renew by owner-a: %v", err)
	}
	if len(renewed) != store.BucketCount() {
		t.Fatalf("owner-a renewed %d buckets, want %d", len(renewed), store.BucketCount())
	}
}

func TestExpiredLeaseReturnsBucketsToCirculation(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})

	if _, err := store.ClaimBuckets(ctx, "owner-a", time.Millisecond); err != nil {
		t.Fatalf("claim by owner-a: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	taken, err := store.ClaimBuckets(ctx, "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("claim by owner-b: %v", err)
	}
	if len(taken) != store.BucketCount() {
		t.Fatalf("owner-b took %d buckets after the lease lapsed, want %d", len(taken), store.BucketCount())
	}
}

func TestPublishBucketPublishesNothingWithoutTheLease(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})

	written := enqueue(t, ctx, store, "subject-1", `{"n":1}`)
	if _, err := store.ClaimBuckets(ctx, "owner-a", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// owner-b holds nothing, so it must publish nothing — and say so without an
	// error, because losing a bucket mid-drain is ordinary, not a failure.
	published, err := store.PublishBucket(ctx, written.Bucket, "owner-b", 10,
		func(context.Context, outbox.Event) error {
			t.Error("published an event from a bucket this owner does not hold")
			return nil
		})
	if err != nil {
		t.Fatalf("publish without lease: %v", err)
	}
	if published != 0 {
		t.Fatalf("published %d events without the lease", published)
	}
}

func TestExhaustedRowMovesToDeadLetters(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{MaxAttempts: 2})
	const owner = "owner-a"

	written := enqueue(t, ctx, store, "subject-1", `{"n":1}`)
	behind := enqueue(t, ctx, store, "subject-1", `{"n":2}`)
	if behind.Bucket != written.Bucket {
		t.Fatalf("test assumes one bucket, got %d and %d", written.Bucket, behind.Bucket)
	}

	if _, err := store.ClaimBuckets(ctx, owner, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	broker := errors.New("broker rejected")
	fail := func(context.Context, outbox.Event) error { return broker }

	// First attempt: counted, row stays put, and the row behind it stays behind
	// it — the ordering guarantee holds even while a row is failing.
	if _, err := store.PublishBucket(ctx, written.Bucket, owner, 10, fail); err == nil {
		t.Fatal("expected the publish failure to surface")
	}
	if got := attemptsOf(t, ctx, store, written); got != 1 {
		t.Fatalf("attempts = %d after one failure, want 1", got)
	}

	// Second attempt exhausts MaxAttempts, so the poison row is parked and the
	// bucket unblocks.
	if _, err := store.PublishBucket(ctx, written.Bucket, owner, 10, fail); err == nil {
		t.Fatal("expected the publish failure to surface")
	}
	if countRows(t, ctx, store, store.dead) != 1 {
		t.Fatal("exhausted row did not reach the dead-letter table")
	}

	got := drain(t, ctx, store, written.Bucket, owner, 10)
	if len(got) != 1 || got[0].ID != behind.ID {
		t.Fatalf("after parking the poison row the bucket should deliver the one behind it, got %+v", got)
	}
}

func TestTransientFailureDoesNotConsumeAttempts(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{MaxAttempts: 2})
	const owner = "owner-a"

	written := enqueue(t, ctx, store, "subject-1", `{"n":1}`)
	if _, err := store.ClaimBuckets(ctx, owner, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	fail := func(context.Context, outbox.Event) error {
		return outbox.Transient(errors.New("kafka unavailable"), 0)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.PublishBucket(ctx, written.Bucket, owner, 10, fail); err == nil {
			t.Fatalf("failure %d did not surface", i+1)
		}
	}
	if got := attemptsOf(t, ctx, store, written); got != 0 {
		t.Fatalf("attempts = %d after transient outage, want 0", got)
	}
	if got := countRows(t, ctx, store, store.dead); got != 0 {
		t.Fatalf("dead rows = %d after transient outage, want 0", got)
	}

	got := drain(t, ctx, store, written.Bucket, owner, 10)
	if len(got) != 1 || got[0].ID != written.ID {
		t.Fatalf("event did not publish after recovery: %+v", got)
	}
}

func TestPermanentFailureMovesDirectlyToDeadLetters(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{MaxAttempts: 10})
	const owner = "owner-a"

	written := enqueue(t, ctx, store, "subject-1", `{"n":1}`)
	behind := enqueue(t, ctx, store, "subject-1", `{"n":2}`)
	if _, err := store.ClaimBuckets(ctx, owner, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	_, err := store.PublishBucket(ctx, written.Bucket, owner, 10,
		func(context.Context, outbox.Event) error {
			return outbox.Permanent(errors.New("invalid event payload"))
		})
	if err == nil {
		t.Fatal("permanent failure did not surface")
	}
	if class := outbox.ClassifyPublishError(err); class != outbox.PublishErrorDeadLettered {
		t.Fatalf("failure class = %q, want dead_lettered Store confirmation", class)
	}
	var permanent *outbox.PermanentPublishError
	if !errors.As(err, &permanent) {
		t.Fatalf("dead-lettered error lost permanent publisher cause: %v", err)
	}
	if got := countRows(t, ctx, store, store.dead); got != 1 {
		t.Fatalf("dead rows = %d, want 1", got)
	}

	got := drain(t, ctx, store, written.Bucket, owner, 10)
	if len(got) != 1 || got[0].ID != behind.ID {
		t.Fatalf("row behind permanent failure did not unblock: %+v", got)
	}
}

func TestDeadLetterInspectionStatsAndIdempotentReplay(t *testing.T) {
	store, ctx := testStore(t, OutboxConfig{})
	const owner = "owner-a"
	if _, err := store.ClaimBuckets(ctx, owner, time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	var written []outbox.Event
	for i := 0; i < 3; i++ {
		ev := enqueue(t, ctx, store, fmt.Sprintf("subject-%d", i), fmt.Sprintf(`{"n":%d}`, i))
		dead, err := store.recordPublishFailure(ctx, ev, owner, outbox.Permanent(errors.New("poison payload")))
		if err != nil {
			t.Fatalf("dead-letter event %d: %v", i, err)
		}
		if !dead {
			t.Fatalf("event %d was not dead-lettered", i)
		}
		written = append(written, ev)
	}

	page1, err := store.ListDeadLetters(ctx, outbox.DeadLetterListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(page1.Items) != 2 || page1.Next == nil {
		t.Fatalf("first page = %+v, want two items and cursor", page1)
	}
	page2, err := store.ListDeadLetters(ctx, outbox.DeadLetterListOptions{Limit: 2, After: page1.Next})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(page2.Items) != 1 || page2.Next != nil {
		t.Fatalf("second page = %+v, want one final item", page2)
	}

	target := page1.Items[0]
	got, err := store.GetDeadLetter(ctx, target.Ref())
	if err != nil {
		t.Fatalf("get dead letter: %v", err)
	}
	if got.ID != target.ID || got.LastError == "" || got.Attempts != 1 {
		t.Fatalf("dead letter = %+v", got)
	}

	stats, err := store.OutboxStats(ctx)
	if err != nil {
		t.Fatalf("stats before replay: %v", err)
	}
	if stats.PendingCount != 0 || stats.DeadCount != 3 || stats.PendingOldestAt != nil || stats.DeadOldestAt == nil {
		t.Fatalf("stats before replay = %+v", stats)
	}

	state, err := store.ReplayDeadLetter(ctx, target.Ref())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if state != outbox.ReplayRequeued {
		t.Fatalf("replay state = %q, want %q", state, outbox.ReplayRequeued)
	}
	state, err = store.ReplayDeadLetter(ctx, target.Ref())
	if err != nil {
		t.Fatalf("repeat replay: %v", err)
	}
	if state != outbox.ReplayAlreadyPending {
		t.Fatalf("repeat replay state = %q, want %q", state, outbox.ReplayAlreadyPending)
	}
	if attempts := attemptsOf(t, ctx, store, target.Event); attempts != 0 {
		t.Fatalf("replayed attempts = %d, want 0", attempts)
	}

	stats, err = store.OutboxStats(ctx)
	if err != nil {
		t.Fatalf("stats after replay: %v", err)
	}
	if stats.PendingCount != 1 || stats.DeadCount != 2 || stats.PendingOldestAt == nil || stats.DeadOldestAt == nil {
		t.Fatalf("stats after replay = %+v", stats)
	}

	published := drain(t, ctx, store, target.Bucket, owner, 10)
	if len(published) != 1 || published[0].ID != target.ID {
		t.Fatalf("replayed event was not published: %+v", published)
	}
	_, err = store.ReplayDeadLetter(ctx, target.Ref())
	var deliveredNotFound outbox.ErrDeadLetterNotFound
	if !errors.As(err, &deliveredNotFound) {
		t.Fatalf("replay after delivery error = %v, want ErrDeadLetterNotFound", err)
	}

	missing := outbox.DeadLetterRef{Bucket: written[0].Bucket, CreatedAt: written[0].CreatedAt, ID: uuid.NewString()}
	_, err = store.GetDeadLetter(ctx, missing)
	var notFound outbox.ErrDeadLetterNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("get missing error = %v, want ErrDeadLetterNotFound", err)
	}
}

func attemptsOf(t *testing.T, ctx context.Context, s *OutboxStore, ev outbox.Event) int32 {
	t.Helper()
	id, err := uuid.Parse(ev.ID)
	if err != nil {
		t.Fatalf("parse event id: %v", err)
	}

	row, err := s.db.Query().QueryRow(ctx, s.q(`
DECLARE $bucket AS Uint8;
DECLARE $created_at AS Timestamp;
DECLARE $id AS Uuid;

SELECT attempts FROM %s
WHERE bucket = $bucket AND created_at = $created_at AND id = $id;`, s.table),
		query.WithParameters(ydbsdk.ParamsBuilder().
			Param("$bucket").Uint8(uint8(ev.Bucket)).
			Param("$created_at").Timestamp(ev.CreatedAt).
			Param("$id").Uuid(id).
			Build()))
	if err != nil {
		t.Fatalf("read attempts: %v", err)
	}

	var attempts int32
	if err := row.Scan(&attempts); err != nil {
		t.Fatalf("scan attempts: %v", err)
	}
	return attempts
}

func countRows(t *testing.T, ctx context.Context, s *OutboxStore, table string) int {
	t.Helper()

	row, err := s.db.Query().QueryRow(ctx, s.q(`SELECT COUNT(*) FROM %s;`, table))
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}

	var n uint64
	if err := row.Scan(&n); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	return int(n)
}
