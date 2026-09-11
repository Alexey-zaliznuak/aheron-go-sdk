package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

func supportedCopyVersion(_ context.Context, version int) error {
	if version != 7 {
		return ErrCopyUnsupportedVersion
	}
	return nil
}

func TestCopyHandlersSignedRoundTrip(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	lookup := v.HandleResourceValues(supportedCopyVersion, ResourceValuesHandlers{
		Lookup: func(_ context.Context, req schemetransfer.LookupRequest) (schemetransfer.LookupResponse, error) {
			if req.IntegrationVersion != 7 || req.ProjectID != "project" || string(req.Parameters["account"]) != `"account-value"` {
				t.Fatalf("lost request context: %+v", req)
			}
			return schemetransfer.LookupResponse{Items: []schemetransfer.ResourceItem{{ID: "identity", Value: json.RawMessage(`{"nativeId":42}`), Title: "Channel", Selectable: true}}}, nil
		},
		Identify: func(_ context.Context, req schemetransfer.LookupRequest) (schemetransfer.IdentifyResponse, error) {
			return schemetransfer.IdentifyResponse{Matches: []schemetransfer.IdentifyMatch{{InputIndex: 0, Status: "missing"}}}, nil
		},
	})
	prepare := v.HandlePrepareCopy(supportedCopyVersion, func(_ context.Context, req schemetransfer.PrepareCopyRequest) (schemetransfer.PrepareCopyResponse, error) {
		if req.SchemeID != "scheme" {
			t.Fatal("lost source scheme")
		}
		return schemetransfer.NewCopyBuilder(map[string]any{"channel": "explicit"}).Resource("/channel", "channels").Build()
	})
	validate := v.HandleValidateCopySettings(supportedCopyVersion, func(context.Context, schemetransfer.ValidateCopySettingsRequest) (schemetransfer.ValidationResult, error) {
		return schemetransfer.ValidationResult{Status: "blocked", Issues: []schemetransfer.Issue{{Severity: "error", Code: "channelIncompatible", Path: "/channel", Message: "Channel does not support these buttons"}}}, nil
	})
	for _, tc := range []struct {
		name           string
		handler        http.Handler
		body, contains string
	}{
		{"search", lookup, `{"mode":"search","projectId":"project","integrationVersion":7,"sourceKey":"channels","parameters":{"account":"account-value"},"limit":1}`, `"nativeId":42`},
		{"resolve", lookup, `{"mode":"resolve","projectId":"project","integrationVersion":7,"sourceKey":"channels","parameters":{"account":"account-value"},"ids":["identity"]}`, `"id":"identity"`},
		{"identify", lookup, `{"mode":"identify","projectId":"project","integrationVersion":7,"sourceKey":"channels","values":["absent"]}`, `"status":"missing"`},
		{"prepare", prepare, `{"protocolVersion":2,"projectId":"project","schemeId":"scheme","integrationVersion":7,"blockKey":"send","settings":{}}`, `"value":"explicit"`},
		{"validate", validate, `{"protocolVersion":2,"projectId":"project","integrationVersion":7,"blockKey":"send","settings":{}}`, `"status":"blocked"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveVariableValues(t, tc.handler, priv, kid, []byte(tc.body))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCopyHandlersRejectBeforeCallback(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	calls := 0
	lookup := func(context.Context, schemetransfer.LookupRequest) (schemetransfer.LookupResponse, error) {
		calls++
		return schemetransfer.LookupResponse{Items: []schemetransfer.ResourceItem{}}, nil
	}
	valid := `{"mode":"search","projectId":"project","integrationVersion":7,"sourceKey":"channels"}`
	for _, tc := range []struct {
		name, body string
		guard      CopyVersionGuard
		status     int
	}{
		{"missing guard", valid, nil, 503},
		{"unsupported version", strings.Replace(valid, `:7`, `:8`, 1), supportedCopyVersion, 409},
		{"duplicate key", strings.Replace(valid, `"mode":"search"`, `"mode":"search","mode":"search"`, 1), supportedCopyVersion, 400},
		{"empty resolve", strings.Replace(valid, `"mode":"search"`, `"mode":"resolve","ids":[]`, 1), supportedCopyVersion, 400},
		{"unknown field", strings.Replace(valid, `"mode":"search"`, `"mode":"search","secret":"not-logged"`, 1), supportedCopyVersion, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveVariableValues(t, v.HandleResourceValues(tc.guard, ResourceValuesHandlers{Lookup: lookup}), priv, kid, []byte(tc.body))
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), "not-logged") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	handler := v.HandleResourceValues(supportedCopyVersion, ResourceValuesHandlers{Lookup: lookup})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resources", strings.NewReader(valid)))
	if rec.Code != 401 || calls != 0 {
		t.Fatalf("unsigned call accepted: %d calls=%d", rec.Code, calls)
	}
	// A valid signed prefix must not authenticate an over-limit body.
	v.maxBody = int64(len(valid))
	req := httptest.NewRequest(http.MethodPost, "/resources", bytes.NewReader([]byte(valid+"trailing-secret")))
	req.Header = signInbound(t, priv, kid, []byte(valid))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 || calls != 0 {
		t.Fatal("authenticated a truncated prefix")
	}
}

func TestCopyHandlersDoNotExposeCallbackErrorsOrInvalidResponses(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	request := []byte(`{"mode":"resolve","projectId":"p","integrationVersion":7,"sourceKey":"channels","ids":["expected"]}`)
	for _, tc := range []struct {
		name   string
		result schemetransfer.LookupResponse
		err    error
		status int
		code   string
	}{
		{"error", schemetransfer.LookupResponse{}, errors.New("secret-token"), 500, "callbackFailed"},
		{"unknown source", schemetransfer.LookupResponse{}, ErrCopyUnknownSource, 404, "unknownSource"},
		{"foreign identity", schemetransfer.LookupResponse{Items: []schemetransfer.ResourceItem{{ID: "secret-token", Value: json.RawMessage(`1`), Title: "Private", Selectable: true}}}, nil, 500, "invalidResponse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := v.HandleResourceValues(supportedCopyVersion, ResourceValuesHandlers{Lookup: func(context.Context, schemetransfer.LookupRequest) (schemetransfer.LookupResponse, error) {
				return tc.result, tc.err
			}})
			rec := serveVariableValues(t, handler, priv, kid, request)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "secret-token") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCopyHandlersRejectRetiredProtocolBeforeDomainCode(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	calls := 0
	guard := func(context.Context, int) error { calls++; return nil }
	prepare := v.HandlePrepareCopy(guard, func(context.Context, schemetransfer.PrepareCopyRequest) (schemetransfer.PrepareCopyResponse, error) {
		calls++
		return schemetransfer.NewCopyBuilder(map[string]any{}).Build()
	})
	validate := v.HandleValidateCopySettings(guard, func(context.Context, schemetransfer.ValidateCopySettingsRequest) (schemetransfer.ValidationResult, error) {
		calls++
		return schemetransfer.ValidationResult{Status: "passed", Issues: []schemetransfer.Issue{}}, nil
	})
	for _, tc := range []struct {
		handler http.Handler
		body    string
	}{
		{prepare, `{"protocolVersion":1,"projectId":"p","schemeId":"s","integrationVersion":7,"blockKey":"send","settings":{}}`},
		{validate, `{"protocolVersion":1,"projectId":"p","integrationVersion":7,"blockKey":"send","settings":{}}`},
	} {
		rec := serveVariableValues(t, tc.handler, priv, kid, []byte(tc.body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("retired protocol: %d", rec.Code)
		}
	}
	if calls != 0 {
		t.Fatal("retired protocol reached version guard or domain code")
	}
}
