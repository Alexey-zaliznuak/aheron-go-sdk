package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeContractsAndVersionedPreparation(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	mode := "ok"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "runtime-contracts") {
			if mode == "duplicate" {
				_, _ = w.Write([]byte(`{"contracts":[{"providerKey":"platform/execution","contractRevision":"native-blocks/1"}],"unavailableProviders":["platform/execution"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"contracts":[{"providerKey":"platform/execution","contractRevision":"native-blocks/1"}],"unavailableProviders":["platform/code-execution"]}`))
			return
		}
		var in struct {
			Expected string `json:"expectedContractRevision"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Expected != "native-blocks/1" {
			t.Errorf("missing expected contract: %+v", in)
		}
		if mode == "conflict" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"contract_revision_mismatch","error":"SECRET"}`))
			return
		}
		if mode == "legacy" {
			_, _ = w.Write([]byte(`{"type":"noop","settings":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"noop","settings":{},"contractRevision":"native-blocks/1"}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, AllowLoopbackHTTP: true, TokenProvider: provider(t, testToken("user"))})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := c.Schemes.RuntimeContracts(context.Background(), id)
	if err != nil || len(profile.Contracts) != 1 || len(profile.UnavailableProviders) != 1 {
		t.Fatalf("%+v %v", profile, err)
	}
	mode = "duplicate"
	if _, err = c.Schemes.RuntimeContracts(context.Background(), id); !errors.Is(err, ErrResponse) {
		t.Fatalf("%v", err)
	}
	mode = "ok"
	if _, err = c.Schemes.PrepareNativeBlockAtContract(context.Background(), id, "noop", json.RawMessage(`{}`), "native-blocks/1"); err != nil {
		t.Fatal(err)
	}
	mode = "legacy"
	if _, err = c.Schemes.PrepareNativeBlockAtContract(context.Background(), id, "noop", json.RawMessage(`{}`), "native-blocks/1"); !errors.Is(err, ErrResponse) {
		t.Fatal("unconfirmed revision accepted")
	}
	mode = "conflict"
	if _, err = c.Schemes.PrepareNativeBlockAtContract(context.Background(), id, "noop", json.RawMessage(`{}`), "native-blocks/1"); !errors.Is(err, ErrContractRevisionMismatch) || !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("%v", err)
	}
	if requests != 5 {
		t.Fatalf("unexpected retry: %d", requests)
	}
}
