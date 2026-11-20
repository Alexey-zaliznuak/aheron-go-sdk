package ydb

import (
	"errors"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

// ServiceConfig scopes tables to directory inside the database named in dsn.
// Managed connections use the service account mounted at /sa.json. Local
// grpc connections use the emulator's anonymous authentication.
func ServiceConfig(dsn, directory string) (tablePathPrefix, serviceAccountKeyFile string, err error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "grpc" && u.Scheme != "grpcs") {
		return "", "", errors.New("ydb: expected a grpc or grpcs database URL without embedded credentials")
	}
	database := strings.TrimSuffix(u.Path, "/")
	if database == "" || path.Clean(database) != database || database == "/" ||
		!fs.ValidPath(directory) || directory == "." {
		return "", "", errors.New("ydb: invalid database path or service directory")
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", errors.New("ydb: invalid connection options")
	}
	for name := range options {
		switch name {
		case "go_query_mode", "go_query_bind", "go_fake_tx":
		default:
			return "", "", errors.New("ydb: unsupported connection option")
		}
	}
	if u.Scheme == "grpcs" {
		serviceAccountKeyFile = "/sa.json"
	}
	return path.Join(database, directory), serviceAccountKeyFile, nil
}
