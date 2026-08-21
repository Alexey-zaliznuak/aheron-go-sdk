package ydb

import (
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/outbox"
)

func TestNowIsTruncatedToMicrosecond(t *testing.T) {
	got := Now()
	if got.Truncate(time.Microsecond) != got {
		t.Fatalf("Now() = %v, carries sub-microsecond precision YDB cannot store", got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("Now() = %v, want UTC", got)
	}
}

func TestOpenRejectsEmptyDSN(t *testing.T) {
	if _, err := Open(t.Context(), "", ""); err == nil {
		t.Fatal("expected an error for an empty dsn")
	}
}

func TestNewOutboxStoreFillsDefaults(t *testing.T) {
	s := NewOutboxStore(nil, OutboxConfig{})

	if s.table != DefaultOutboxTable {
		t.Fatalf("table = %q, want %q", s.table, DefaultOutboxTable)
	}
	if s.leases != DefaultOutboxTable+"_leases" || s.dead != DefaultOutboxTable+"_dead" {
		t.Fatalf("derived tables = %q, %q", s.leases, s.dead)
	}
	if s.BucketCount() != outbox.DefaultBucketCount {
		t.Fatalf("BucketCount() = %d, want %d", s.BucketCount(), outbox.DefaultBucketCount)
	}
	if s.maxAttempts != DefaultMaxAttempts {
		t.Fatalf("maxAttempts = %d, want %d", s.maxAttempts, DefaultMaxAttempts)
	}
	if s.pragma != "" {
		t.Fatalf("pragma = %q, want empty when no table path prefix is configured", s.pragma)
	}
}

func TestNewOutboxStoreDerivesTableNames(t *testing.T) {
	s := NewOutboxStore(nil, OutboxConfig{
		TablePathPrefix: "/local/payments",
		Table:           "dialog_outbox",
		BucketCount:     8,
		MaxAttempts:     3,
	})

	if s.leases != "dialog_outbox_leases" || s.dead != "dialog_outbox_dead" {
		t.Fatalf("derived tables = %q, %q", s.leases, s.dead)
	}
	if s.BucketCount() != 8 || s.maxAttempts != 3 {
		t.Fatalf("config not applied: buckets=%d attempts=%d", s.BucketCount(), s.maxAttempts)
	}
	if s.pragma != "PRAGMA TablePathPrefix(\"/local/payments\");\n" {
		t.Fatalf("pragma = %q", s.pragma)
	}
}

func TestOptionalText(t *testing.T) {
	if optionalText("") != nil {
		t.Fatal("an empty string must be stored as NULL, not as \"\"")
	}
	if got := optionalText("topic"); got == nil || *got != "topic" {
		t.Fatalf("optionalText(%q) = %v", "topic", got)
	}
}
