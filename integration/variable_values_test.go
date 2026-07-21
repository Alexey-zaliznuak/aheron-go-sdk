package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newVariableValuesVerifier(t *testing.T) (*Verifier, ed25519.PrivateKey, string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const kid = "variable-values-test"
	jwks := platformJWKS(kid, pub)
	t.Cleanup(jwks.Close)

	verifier, err := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return verifier, priv, kid
}

func serveVariableValues(
	t *testing.T,
	handler http.Handler,
	priv ed25519.PrivateKey,
	kid string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/variable-values", bytes.NewReader(body))
	req.Header = signInbound(t, priv, kid, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandleVariableValuesRejectsUnauthenticatedRequest(t *testing.T) {
	verifier, _, _ := newVariableValuesVerifier(t)
	called := false
	handler := verifier.HandleVariableValues(func(context.Context, VariableValuesRequest) (VariableValuesResponse, error) {
		called = true
		return VariableValuesResponse{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/variable-values", strings.NewReader(
		`{"projectId":"p1","variableKey":"channel"}`,
	))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("callback was called for unauthenticated request")
	}
}

func TestHandleVariableValuesRejectsMalformedBody(t *testing.T) {
	verifier, priv, kid := newVariableValuesVerifier(t)
	called := false
	handler := verifier.HandleVariableValues(func(context.Context, VariableValuesRequest) (VariableValuesResponse, error) {
		called = true
		return VariableValuesResponse{}, nil
	})

	rec := serveVariableValues(t, handler, priv, kid, []byte(`{"projectId":`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("callback was called for malformed request")
	}
}

func TestHandleVariableValuesValidatesRequest(t *testing.T) {
	tooManyValues := make([]string, maxVariableValuesItems+1)
	for i := range tooManyValues {
		tooManyValues[i] = "value"
	}

	tests := []struct {
		name string
		body any
	}{
		{name: "missing project", body: map[string]any{"variableKey": "channel"}},
		{name: "missing variable key", body: map[string]any{"projectId": "p1"}},
		{name: "zero limit", body: map[string]any{"projectId": "p1", "variableKey": "channel", "limit": 0}},
		{name: "limit above cap", body: map[string]any{"projectId": "p1", "variableKey": "channel", "limit": maxVariableValuesItems + 1}},
		{name: "empty cursor", body: map[string]any{"projectId": "p1", "variableKey": "channel", "cursor": " "}},
		{name: "values above cap", body: map[string]any{"projectId": "p1", "variableKey": "channel", "values": tooManyValues}},
		{name: "search resolve conflict", body: map[string]any{"projectId": "p1", "variableKey": "channel", "query": "gen", "values": []string{"general"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, priv, kid := newVariableValuesVerifier(t)
			called := false
			handler := verifier.HandleVariableValues(func(context.Context, VariableValuesRequest) (VariableValuesResponse, error) {
				called = true
				return VariableValuesResponse{}, nil
			})
			body, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}

			rec := serveVariableValues(t, handler, priv, kid, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
			if called {
				t.Fatal("callback was called for invalid request")
			}
		})
	}
}

func TestHandleVariableValuesSearchSuccess(t *testing.T) {
	verifier, priv, kid := newVariableValuesVerifier(t)
	icon := "https://example.test/general.png"
	nextCursor := "page-2"
	var captured VariableValuesRequest
	handler := verifier.HandleVariableValues(func(_ context.Context, req VariableValuesRequest) (VariableValuesResponse, error) {
		captured = req
		return VariableValuesResponse{
			Items: []VariableValueItem{{
				Value: "general",
				Title: "General",
				Icon:  &icon,
			}},
			NextCursor: &nextCursor,
		}, nil
	})
	body := []byte(`{"projectId":"p1","variableKey":"channel","query":"gen","cursor":"page-1","limit":10}`)

	rec := serveVariableValues(t, handler, priv, kid, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if captured.ProjectID != "p1" || captured.VariableKey != "channel" {
		t.Fatalf("decoded request mismatch: %+v", captured)
	}
	if captured.Query == nil || *captured.Query != "gen" || captured.Cursor == nil || *captured.Cursor != "page-1" {
		t.Fatalf("search fields mismatch: %+v", captured)
	}
	if captured.Limit == nil || *captured.Limit != 10 {
		t.Fatalf("limit mismatch: %+v", captured.Limit)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var response VariableValuesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Value != "general" || response.Items[0].Icon == nil {
		t.Fatalf("response mismatch: %+v", response)
	}
	if response.NextCursor == nil || *response.NextCursor != "page-2" {
		t.Fatalf("next cursor mismatch: %+v", response.NextCursor)
	}
	if strings.Contains(rec.Body.String(), "NextCursor") || !strings.Contains(rec.Body.String(), `"nextCursor"`) {
		t.Fatalf("response is not camelCase JSON: %s", rec.Body.String())
	}
}

func TestHandleVariableValuesResolveSuccess(t *testing.T) {
	verifier, priv, kid := newVariableValuesVerifier(t)
	handler := verifier.HandleVariableValues(func(_ context.Context, req VariableValuesRequest) (VariableValuesResponse, error) {
		if len(req.Values) != 2 || req.Values[0] != "general" {
			t.Fatalf("resolved values mismatch: %v", req.Values)
		}
		return VariableValuesResponse{Items: []VariableValueItem{
			{Value: "general", Title: "General"},
			{Value: "random", Title: "Random"},
		}}, nil
	})
	body := []byte(`{"projectId":"p1","variableKey":"channel","values":["general","random"]}`)

	rec := serveVariableValues(t, handler, priv, kid, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleVariableValuesHandlesCallbackError(t *testing.T) {
	verifier, priv, kid := newVariableValuesVerifier(t)
	handler := verifier.HandleVariableValues(func(context.Context, VariableValuesRequest) (VariableValuesResponse, error) {
		return VariableValuesResponse{}, errors.New("upstream unavailable")
	})
	body := []byte(`{"projectId":"p1","variableKey":"channel"}`)

	rec := serveVariableValues(t, handler, priv, kid, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "upstream unavailable") {
		t.Fatalf("callback error leaked to caller: %s", rec.Body.String())
	}
}

func TestHandleVariableValuesValidatesResponse(t *testing.T) {
	emptyCursor := " "
	tests := []struct {
		name     string
		body     string
		response VariableValuesResponse
	}{
		{
			name:     "empty value",
			body:     `{"projectId":"p1","variableKey":"channel"}`,
			response: VariableValuesResponse{Items: []VariableValueItem{{Title: "General"}}},
		},
		{
			name:     "empty title",
			body:     `{"projectId":"p1","variableKey":"channel"}`,
			response: VariableValuesResponse{Items: []VariableValueItem{{Value: "general"}}},
		},
		{
			name:     "empty next cursor",
			body:     `{"projectId":"p1","variableKey":"channel"}`,
			response: VariableValuesResponse{NextCursor: &emptyCursor},
		},
		{
			name: "more items than requested",
			body: `{"projectId":"p1","variableKey":"channel","limit":1}`,
			response: VariableValuesResponse{Items: []VariableValueItem{
				{Value: "general", Title: "General"},
				{Value: "random", Title: "Random"},
			}},
		},
		{
			name:     "cursor in resolve response",
			body:     `{"projectId":"p1","variableKey":"channel","values":["general"]}`,
			response: VariableValuesResponse{Items: []VariableValueItem{{Value: "general", Title: "General"}}, NextCursor: stringPointer("next")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, priv, kid := newVariableValuesVerifier(t)
			handler := verifier.HandleVariableValues(func(context.Context, VariableValuesRequest) (VariableValuesResponse, error) {
				return tt.response, nil
			})

			rec := serveVariableValues(t, handler, priv, kid, []byte(tt.body))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("want 500, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
