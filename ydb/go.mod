// The YDB half of the SDK is a separate module so that ydb-go-sdk and its gRPC
// dependency tree never reach an integration that runs on PostgreSQL.
//
// It depends on the parent module for the outbox contract, so the two are
// released in order: tag the parent first, bump the require below, then tag
// ydb/vX.Y.Z. Local builds resolve the parent from disk through go.work.
//
// google.golang.org/genproto is required explicitly and is not indirect
// bookkeeping. It used to ship googleapis/api and googleapis/rpc itself and
// later split them into their own modules; ydb-go-yc drags in yandex-cloud/
// go-genproto, which still asks for a pre-split version. In workspace mode the
// module graph is not pruned, both copies land in the build list, and every
// import of those packages becomes ambiguous. Selecting a post-split version
// leaves exactly one provider.
//
// `go mod tidy` drops that line, because no package here imports it directly.
// `task tidy` therefore restores it as its next command; do not remove either.
module github.com/Alexey-zaliznuak/aheron-go-sdk/ydb

go 1.25.0

require (
	github.com/Alexey-zaliznuak/aheron-go-sdk v0.32.0
	github.com/google/uuid v1.6.0
	github.com/ydb-platform/ydb-go-genproto v0.0.0-20260810122915-65bfd5c4b705
	github.com/ydb-platform/ydb-go-sdk/v3 v3.150.1
	github.com/ydb-platform/ydb-go-yc v0.12.4
)

require (
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/yandex-cloud/go-genproto v0.0.0-20240819112322-98a264d392f6 // indirect
	github.com/ydb-platform/ydb-go-yc-metadata v0.6.1 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto v0.0.0-20260819154853-08b0e4226688
	google.golang.org/genproto/googleapis/api v0.0.0-20260818201246-1b0934165a6f // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260818201246-1b0934165a6f // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
