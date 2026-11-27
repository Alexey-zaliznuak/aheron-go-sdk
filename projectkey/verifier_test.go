package projectkey

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestVerifier(t *testing.T) {
	project := uuid.New()
	lookupToken := strings.Repeat("s", 32)
	const key = "ahr_proj_sample"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Token string }
		if r.Method != http.MethodPost || r.URL.Path != "/internal/api-keys/lookup" ||
			r.Header.Get("X-Project-Key-Lookup-Token") != lookupToken ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body.Token != key {
			t.Error("wrong lookup request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch calls.Add(1) {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"projectId": project, "valid": true, "scopes": []string{}})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{"projectId": project, "valid": false})
		case 3:
			w.WriteHeader(http.StatusNotFound)
		default:
			http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
		}
	}))
	defer upstream.Close()

	verifier, err := NewVerifier(upstream.URL, lookupToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := verifier.Verify(t.Context(), key); err != nil || got != project {
		t.Fatalf("valid lookup = %v, %v", got, err)
	}
	for _, want := range []error{ErrInvalidKey, ErrInvalidKey, ErrUnavailable} {
		if got, err := verifier.Verify(t.Context(), key); !errors.Is(err, want) || got != uuid.Nil {
			t.Fatalf("lookup = %v, %v, want %v", got, err, want)
		}
	}
	if _, err := verifier.Verify(t.Context(), "user-jwt"); !errors.Is(err, ErrInvalidKey) || calls.Load() != 4 {
		t.Fatalf("non-project credential lookup: %v, calls = %d", err, calls.Load())
	}
}
