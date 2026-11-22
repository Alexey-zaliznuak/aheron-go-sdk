package aheron

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

func TestProjectKeyNamedTriggerActivation(t *testing.T) {
	for _, payload := range []string{"", "null", "false", "[9007199254740993]"} {
		t.Run(payload, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/execution/triggers/activate" ||
					r.Header.Get("Authorization") != "Bearer ahr_proj_example" {
					t.Errorf("request: %s %s", r.Method, r.URL.Path)
				}
				var input platform.NamedTriggerActivation
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil ||
					input.ActivationKey != "abcdefghijklmnop" || input.SubjectID != "11111111-1111-4111-8111-000000000001" ||
					string(input.Payload) != payload {
					t.Errorf("input: %+v, payload=%s, err=%v", input, input.Payload, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"executionContextIds":["context"]}`))
			}))
			defer server.Close()
			client, err := New(Config{ProjectAPIKey: "ahr_proj_example", BaseURL: server.URL + "/api", AllowLoopbackHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.NamedTriggers.Activate(context.Background(), platform.NamedTriggerActivation{
				ActivationKey: "abcdefghijklmnop",
				SubjectID:     "11111111-1111-4111-8111-000000000001",
				Payload:       json.RawMessage(payload),
			})
			if err != nil || len(result.ExecutionContextIDs) != 1 || result.ExecutionContextIDs[0] != "context" {
				t.Fatalf("result: %+v, err=%v", result, err)
			}
		})
	}
}
