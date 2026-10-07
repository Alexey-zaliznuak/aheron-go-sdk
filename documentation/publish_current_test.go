package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationPublicationChecksBothChannelsAndRecovers(t *testing.T) {
	for _, upload := range []bool{true, false} {
		for _, mode := range []string{"new", "retry", "lost-response", "current-race", "superseded", "wrong-current-receipt", "legacy-receipt", "current-revoked"} {
			name := "activate/" + mode
			if upload {
				name = "publish/" + mode
			}
			t.Run(name, func(t *testing.T) {
				p := fixture()
				p.ProviderKey = "integration/11111111-1111-4111-8111-111111111111"
				_, raw, _ := CanonicalPackage(p)
				exact := Channel{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: SHA256(raw), Revision: 8}
				current := exact
				current.Revision = 5
				receipt := Receipt{OperationID: "pipeline-42", Channel: exact, CurrentChannel: &current}
				committed := mode == "retry" || mode == "superseded" || mode == "wrong-current-receipt" || mode == "legacy-receipt"
				activations, uploads := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/public/") && r.Header.Get("Authorization") != "" {
						t.Error("publisher token leaked into public catalog")
					}
					switch r.URL.Path {
					case "/publishing/receipts/pipeline-42":
						if !committed {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						out := receipt
						if mode == "legacy-receipt" {
							out.CurrentChannel = nil
						} else if mode == "wrong-current-receipt" {
							wrong := current
							wrong.ProviderKey = "integration/22222222-2222-4222-8222-222222222222"
							out.CurrentChannel = &wrong
						}
						_ = json.NewEncoder(w).Encode(out)
					case "/publishing/packages":
						uploads++
						_ = json.NewEncoder(w).Encode(UploadResult{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: exact.PackageDigest})
					case "/public/knowledge/catalog":
						channel := exact
						if !committed {
							channel.Revision = 7
						}
						_ = json.NewEncoder(w).Encode(Catalog{Channel: channel, SourceRevision: p.SourceRevision})
					case "/public/knowledge/current/catalog":
						if mode == "current-revoked" {
							w.WriteHeader(http.StatusGone)
							return
						}
						channel := current
						if !committed {
							channel.ContractRevision, channel.Revision = "previous-contract/1", 4
						}
						if mode == "superseded" {
							channel.Revision++
						}
						_ = json.NewEncoder(w).Encode(Catalog{Channel: channel, SourceRevision: p.SourceRevision})
					case "/publishing/activate":
						activations++
						var in ActivateRequest
						if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ExpectedCurrentRevision == nil || *in.ExpectedCurrentRevision != 4 || in.ExpectedRevision != 7 || in.PackageDigest != exact.PackageDigest {
							t.Errorf("lost independent CAS: %+v %v", in, err)
						}
						if mode == "current-race" {
							w.WriteHeader(http.StatusConflict)
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
				c, err := New(Config{BaseURL: server.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { return "ci-token", nil }})
				if err != nil {
					t.Fatal(err)
				}
				publish := c.ActivateCurrentPackage
				if upload {
					publish = c.PublishCurrent
				}
				_, err = publish(context.Background(), p, receipt.OperationID)
				success := mode == "new" || mode == "retry" || mode == "lost-response"
				if (err == nil) != success || activations > 1 || (!upload && uploads != 0) {
					t.Fatalf("activations=%d uploads=%d error=%v", activations, uploads, err)
				}
				if (mode == "retry" || mode == "superseded" || mode == "wrong-current-receipt" || mode == "legacy-receipt") && (uploads != 0 || activations != 0) {
					t.Fatal("receipt recovery changed publication")
				}
				if mode == "current-race" && !apiStatus(err, http.StatusConflict) {
					t.Fatalf("lost current CAS conflict: %v", err)
				}
			})
		}
	}
}

func TestCurrentPublicationRejectsPlatformProviderBeforeHTTP(t *testing.T) {
	if _, err := new(Client).PublishCurrent(context.Background(), fixture(), "pipeline-42"); err == nil {
		t.Fatal("platform provider accepted for current publication")
	}
}
