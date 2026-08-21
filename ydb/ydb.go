// Package ydb holds the YDB half of the SDK: connecting to a managed cluster
// and a ready-made outbox store on the standard outbox schema.
//
// It is a separate Go module on purpose. Most integrations run on PostgreSQL
// and want nothing from here, and a nested module keeps ydb-go-sdk and its gRPC
// dependency tree out of their go.sum entirely.
package ydb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb"
	ydbsdk "github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/query"
	yc "github.com/ydb-platform/ydb-go-yc"
)

// Open connects to YDB.
//
// serviceAccountKeyFile is the authorized key of the service account the caller
// runs as. An empty path means an anonymous connection, which is what a local
// docker cluster expects.
//
// Authorizing by a service account key is Yandex-Cloud-specific and lives
// outside the core driver, in ydb-go-yc. WithInternalCA comes with it and is
// not optional against a managed cluster: its certificate is signed by the
// cloud's own CA, and without it the TLS handshake fails.
func Open(ctx context.Context, dsn, serviceAccountKeyFile string, opts ...ydbsdk.Option) (*ydbsdk.Driver, error) {
	if dsn == "" {
		return nil, errors.New("ydb: empty dsn")
	}

	all := make([]ydbsdk.Option, 0, len(opts)+2)
	if serviceAccountKeyFile != "" {
		all = append(all,
			yc.WithInternalCA(),
			yc.WithServiceAccountKeyFileCredentials(serviceAccountKeyFile),
		)
	}
	all = append(all, opts...)

	db, err := ydbsdk.Open(ctx, dsn, all...)
	if err != nil {
		return nil, fmt.Errorf("ydb: open: %w", err)
	}
	return db, nil
}

// Now is the source of time for rows written through this package.
//
// YDB stores Timestamp with microsecond resolution, so a value written by
// time.Now() is not the value that comes back on the next read. Truncating up
// front keeps what a write returns identical to what a later read returns —
// which matters here because the outbox addresses rows by created_at.
func Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// IsNotFound reports whether err came from a query that produced no rows. The
// driver surfaces that as io.EOF rather than a dedicated status.
func IsNotFound(err error) bool {
	return errors.Is(err, io.EOF)
}

// IsConflict reports whether err came from an INSERT onto an occupied primary
// key. YDB answers PRECONDITION_FAILED, which is how a uniqueness table detects
// a taken key.
func IsConflict(err error) bool {
	return ydbsdk.IsOperationError(err, Ydb.StatusIds_PRECONDITION_FAILED)
}

// pathPrefix renders the PRAGMA that scopes bare table names to a service's own
// directory. The directory differs between environments — /local against the
// docker image, /ru-central1/<cloud>/<dbid>/<service> in the cloud — so it
// comes from configuration and never from the query text. An empty prefix means
// the connection string already carries table_path_prefix.
func pathPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf("PRAGMA TablePathPrefix(%q);\n", prefix)
}

func collect[T any](ctx context.Context, rs query.ResultSet, scan func(query.Row) (T, error)) ([]T, error) {
	items := make([]T, 0)
	for {
		row, err := rs.NextRow(ctx)
		if errors.Is(err, io.EOF) {
			return items, nil
		}
		if err != nil {
			return nil, err
		}
		item, err := scan(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
}
