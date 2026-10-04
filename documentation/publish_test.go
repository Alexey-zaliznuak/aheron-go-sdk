package docs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishRecoveryAndConcurrency(t *testing.T) {
	for _, mode := range []string{"new", "existing", "lost-response", "retry", "superseded", "wrong-receipt", "race", "revoked", "unauthorized", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture()
			_, raw, _ := CanonicalPackage(p)
			receipt := Receipt{OperationID: "pipeline-42", Channel: Channel{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: SHA256(raw), Revision: 1}}
			committed := mode == "retry" || mode == "superseded" || mode == "wrong-receipt"
			initialRevision := int64(0)
			if mode == "existing" || mode == "race" {
				initialRevision = 7
				receipt.Channel.Revision = 8
			}
			activations, uploads := 0, 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				problem := func(status int) { w.WriteHeader(status); _, _ = w.Write([]byte(`{"error":{"code":"test"}}`)) }
				if strings.HasPrefix(r.URL.Path, "/publishing/") && r.Header.Get("Authorization") != "Bearer ci-token" {
					t.Error("missing CI credential")
				}
				if strings.HasPrefix(r.URL.Path, "/public/") && r.Header.Get("Authorization") != "" {
					t.Error("credential leaked to public API")
				}
				switch r.URL.Path {
				case "/publishing/receipts/pipeline-42":
					if mode == "unauthorized" {
						problem(401)
						return
					}
					if !committed {
						problem(404)
						return
					}
					out := receipt
					if mode == "wrong-receipt" {
						out.Channel.PackageDigest = strings.Repeat("b", 64)
					}
					_ = json.NewEncoder(w).Encode(out)
				case "/publishing/packages":
					uploads++
					_ = json.NewEncoder(w).Encode(UploadResult{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: SHA256(raw)})
				case "/public/knowledge/catalog":
					if mode == "revoked" {
						problem(410)
						return
					}
					if !committed && initialRevision == 0 {
						problem(404)
						return
					}
					channel := receipt.Channel
					if !committed {
						channel.Revision = initialRevision
					}
					if mode == "superseded" {
						channel.Revision++
					}
					_ = json.NewEncoder(w).Encode(Catalog{Channel: channel, SourceRevision: p.SourceRevision})
				case "/publishing/activate":
					activations++
					var req ActivateRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.ExpectedRevision != initialRevision || req.OperationID != receipt.OperationID || req.PackageDigest != SHA256(raw) {
						t.Errorf("wrong activation: %+v", req)
					}
					if mode == "race" {
						problem(409)
						return
					}
					committed = true
					if mode == "lost-response" {
						problem(503)
						return
					}
					_ = json.NewEncoder(w).Encode(receipt)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					problem(500)
				}
			}))
			defer s.Close()
			client, _ := New(Config{BaseURL: s.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { return "ci-token", nil }})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			got, err := client.Publish(ctx, p, receipt.OperationID)
			wantSuccess := mode == "new" || mode == "existing" || mode == "lost-response" || mode == "retry"
			if (err == nil) != wantSuccess {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
			if wantSuccess && got != receipt {
				t.Fatalf("wrong receipt: %+v", got)
			}
			if activations > 1 {
				t.Fatal("retried CAS")
			}
			if (mode == "retry" || mode == "superseded" || mode == "wrong-receipt" || mode == "unauthorized" || mode == "cancelled") && (uploads != 0 || activations != 0) {
				t.Fatal("recovery or auth failure mutated channel")
			}
			if mode == "race" && !apiStatus(err, 409) {
				t.Fatalf("lost conflict: %v", err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
