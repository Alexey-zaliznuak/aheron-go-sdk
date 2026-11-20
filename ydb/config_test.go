package ydb

import "testing"

func TestServiceConfig(t *testing.T) {
	for _, tc := range []struct{ dsn, prefix, key string }{
		{"grpcs://example:2135/region/cloud/db", "/region/cloud/db/integrations/payments", "/sa.json"},
		{"grpc://localhost:2136/local?go_query_mode=scripting&go_query_bind=declare,numeric", "/local/integrations/payments", ""},
	} {
		prefix, key, err := ServiceConfig(tc.dsn, "integrations/payments")
		if err != nil || prefix != tc.prefix || key != tc.key {
			t.Errorf("got (%q, %q, %v)", prefix, key, err)
		}
	}
	for _, dsn := range []string{"grpcs://user:pass@example/db", "grpcs://example/db?token=abc", "grpc://localhost", "http://example/db"} {
		if _, _, err := ServiceConfig(dsn, "payments"); err == nil {
			t.Error("accepted invalid connection string")
		}
	}
}
