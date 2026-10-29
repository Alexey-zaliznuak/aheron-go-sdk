package integrationoauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidIntegrationToken     = errors.New("invalid integration OAuth token")
	ErrIntegrationOAuthUnavailable = errors.New("integration OAuth authorization unavailable")
	ErrIntegrationOAuthConfig      = errors.New("invalid integration OAuth configuration")
)

// ResourcePrincipal is obtained only from online auth-service introspection.
// It is distinct from user JWTs and project keys. No bearer is kept in context.
type ResourcePrincipal struct {
	ClientID       string
	ProjectID      string
	IntegrationID  string
	InstallationID string
	Scopes         []string
	ExpiresAt      time.Time
}

type integrationPrincipalKey struct{}

func ContextWithResourcePrincipal(ctx context.Context, p ResourcePrincipal) context.Context {
	p.Scopes = slices.Clone(p.Scopes)
	return context.WithValue(ctx, integrationPrincipalKey{}, p)
}

func ResourcePrincipalFromContext(ctx context.Context) (ResourcePrincipal, bool) {
	p, ok := ctx.Value(integrationPrincipalKey{}).(ResourcePrincipal)
	p.Scopes = slices.Clone(p.Scopes)
	return p, ok
}

func (p ResourcePrincipal) HasScope(scope string) bool { return slices.Contains(p.Scopes, scope) }

type ResourceVerifier struct {
	endpoint, internalToken, audience string
	client                            *http.Client
}

// NewResourceVerifier binds introspection to one resource audience on the internal auth listener. This client
// has no cache, cookies, redirects or retries and sends a fixed resource audience.
func NewResourceVerifier(baseURL, internalToken, audience string, timeout time.Duration) (*ResourceVerifier, error) {
	u, err := url.Parse(baseURL)
	if audience == "" || len(audience) > 128 || strings.ContainsAny(audience, " \r\n\t") || err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") || strings.TrimSpace(internalToken) == "" || strings.ContainsAny(internalToken, "\r\n") || timeout <= 0 || timeout > 10*time.Second {
		return nil, ErrIntegrationOAuthConfig
	}
	return &ResourceVerifier{endpoint: strings.TrimRight(baseURL, "/") + "/internal/oauth/introspect", internalToken: internalToken, audience: audience,
		client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (v *ResourceVerifier) Verify(ctx context.Context, raw string) (ResourcePrincipal, error) {
	if !validResourceBearer(raw) {
		return ResourcePrincipal{}, ErrInvalidIntegrationToken
	}
	if v == nil || v.client == nil {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	body := url.Values{"token": {raw}, "audience": {v.audience}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(body))
	if err != nil {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Internal-Token", v.internalToken)
	resp, err := v.client.Do(req)
	if err != nil {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	if err != nil || len(rawBody) > 16<<10 {
		return ResourcePrincipal{}, ErrIntegrationOAuthUnavailable
	}
	return decodeResourceIntrospection(rawBody, time.Now(), v.audience)
}

func decodeResourceIntrospection(raw []byte, now time.Time, audience string) (ResourcePrincipal, error) {
	invalid := ErrIntegrationOAuthUnavailable
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return ResourcePrincipal{}, invalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		name, err := d.Token()
		key, ok := name.(string)
		if err != nil || !ok {
			return ResourcePrincipal{}, invalid
		}
		if _, exists := fields[key]; exists {
			return ResourcePrincipal{}, invalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ResourcePrincipal{}, invalid
		}
		fields[key] = value
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		return ResourcePrincipal{}, invalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ResourcePrincipal{}, invalid
	}
	var active bool
	if json.Unmarshal(fields["active"], &active) != nil {
		return ResourcePrincipal{}, invalid
	}
	if !active {
		if len(fields) != 1 {
			return ResourcePrincipal{}, invalid
		}
		return ResourcePrincipal{}, ErrInvalidIntegrationToken
	}
	// Standard OAuth names are intentional; the installation extensions are camelCase.
	var wire struct {
		Active         bool   `json:"active"`
		ClientID       string `json:"client_id"`
		Type           string `json:"token_type"`
		Scope          string `json:"scope"`
		Audience       string `json:"aud"`
		Subject        string `json:"sub"`
		Expires        int64  `json:"exp"`
		Issued         int64  `json:"iat"`
		ProjectID      string `json:"projectId"`
		IntegrationID  string `json:"integrationId"`
		InstallationID string `json:"installationId"`
	}
	required := []string{"active", "client_id", "token_type", "scope", "aud", "sub", "exp", "iat", "projectId", "integrationId", "installationId"}
	if len(fields) != len(required) {
		return ResourcePrincipal{}, invalid
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return ResourcePrincipal{}, invalid
		}
	}
	if json.Unmarshal(raw, &wire) != nil || !validResourceID(wire.ClientID) || !validResourceID(wire.ProjectID) || !validResourceID(wire.IntegrationID) || !validResourceID(wire.InstallationID) || wire.Subject != wire.InstallationID || wire.Audience != audience || wire.Type != "Bearer" || wire.Issued <= 0 || wire.Issued > now.Unix() || wire.Expires <= wire.Issued || wire.Expires-wire.Issued > 300 {
		return ResourcePrincipal{}, invalid
	}
	if wire.Expires <= now.Unix() {
		return ResourcePrincipal{}, ErrInvalidIntegrationToken
	}
	scopes := strings.Split(wire.Scope, " ")
	seen := map[string]bool{}
	if len(scopes) > 128 {
		return ResourcePrincipal{}, invalid
	}
	for _, scope := range scopes {
		if len(scope) == 0 || len(scope) > 128 || seen[scope] {
			return ResourcePrincipal{}, invalid
		}
		for _, c := range scope {
			if c < 33 || c > 126 || c == '"' || c == '\\' {
				return ResourcePrincipal{}, invalid
			}
		}
		seen[scope] = true
	}
	return ResourcePrincipal{ClientID: wire.ClientID, ProjectID: wire.ProjectID, IntegrationID: wire.IntegrationID, InstallationID: wire.InstallationID, Scopes: scopes, ExpiresAt: time.Unix(wire.Expires, 0)}, nil
}

func validResourceBearer(raw string) bool {
	if len(raw) != 47 || !strings.HasPrefix(raw, "aho_") {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw[4:])
	return err == nil && len(decoded) == 32
}

func validResourceID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
