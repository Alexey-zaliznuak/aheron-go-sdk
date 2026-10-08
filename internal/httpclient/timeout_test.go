package httpclient

import (
	"testing"
	"time"
)

func TestExplicitTimeoutOptOut(t *testing.T) {
	if c := New(Config{}); c.r.GetClient().Timeout != DefaultTimeout {
		t.Fatal("legacy default changed")
	}
	if c := New(Config{Timeout: time.Nanosecond, DisableTimeout: true}); c.r.GetClient().Timeout != 0 {
		t.Fatal("timeout still enabled")
	}
}
