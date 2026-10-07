package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActivatePackageNeverUploadsAndRecoversReceipt(t *testing.T) {
	for _, mode := range []string{"uploaded", "missing", "retry", "lost-response", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture()
			_, raw, _ := CanonicalPackage(p)
			receipt := Receipt{OperationID: "pipeline-42", Channel: Channel{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: SHA256(raw), Revision: 1}}
			committed := mode == "retry" || mode == "superseded"
			activations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/publishing/packages":
					t.Error("activate phase tried to upload; artifact must already be staged")
					w.WriteHeader(http.StatusInternalServerError)
				case "/publishing/receipts/pipeline-42":
					if !committed {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(receipt)
				case "/public/knowledge/catalog":
					if !committed {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					channel := receipt.Channel
					if mode == "superseded" {
						channel.Revision++
					}
					_ = json.NewEncoder(w).Encode(Catalog{Channel: channel, SourceRevision: p.SourceRevision})
				case "/publishing/activate":
					activations++
					var in ActivateRequest
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.PackageDigest != receipt.Channel.PackageDigest || in.ExpectedRevision != 0 || in.OperationID != receipt.OperationID {
						t.Errorf("bad activation %+v: %v", in, err)
					}
					if mode == "missing" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					committed = true
					if mode == "lost-response" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_ = json.NewEncoder(w).Encode(receipt)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { return "ci-token", nil }})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ActivatePackage(context.Background(), p, receipt.OperationID)
			if success := mode != "missing" && mode != "superseded"; (err == nil) != success {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
			if activations > 1 || ((mode == "retry" || mode == "superseded") && activations != 0) {
				t.Fatalf("activation retries: %d", activations)
			}
		})
	}
}
