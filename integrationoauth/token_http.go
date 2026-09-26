package integrationoauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrToken = errors.New("integration OAuth: token acquisition failed")

// TokenError contains only fixed error classifications and the HTTP status.
// Upstream text, response bodies, assertions, URLs and credentials are omitted.
type TokenError struct {
	StatusCode int
	Code       string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("%s: %s (HTTP %d)", ErrToken, e.Code, e.StatusCode)
}
func (e *TokenError) Unwrap() error { return ErrToken }

func (p *Provider) assertion(now time.Time) (string, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", &TokenError{Code: "entropy"}
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": p.keyID})
	claims, _ := json.Marshal(struct {
		Issuer    string `json:"iss"`
		Subject   string `json:"sub"`
		Audience  string `json:"aud"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		ID        string `json:"jti"`
	}{p.clientID, p.clientID, p.endpoint, now.Unix(), now.Unix() + 60, base64.RawURLEncoding.EncodeToString(entropy[:])})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(p.privateKey, []byte(input))), nil
}

func (p *Provider) fetch(ctx context.Context, key cacheKey) (Token, time.Time, error) {
	started := p.now()
	assertion, err := p.assertion(started)
	if err != nil {
		return Token{}, time.Time{}, err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {p.clientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion}, "projectId": {key.project}, "installationId": {key.installation},
		"audience": {key.audience}, "scope": {key.scopes}}
	if key.kind == "application" {
		form.Del("projectId")
		form.Del("installationId")
		form.Del("scope")
		form.Set("tokenKind", "application")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, time.Time{}, ErrConfig
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// No retries here: a committed assertion is single-use. A later acquisition
	// signs a fresh jti, including after an ambiguous timeout/503 response.
	response, err := p.http.Do(req)
	if err != nil {
		return Token{}, time.Time{}, tokenTransportError(ctx, "transport")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Token{}, time.Time{}, &TokenError{StatusCode: response.StatusCode, Code: "http_status"}
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return Token{}, time.Time{}, &TokenError{StatusCode: 200, Code: "invalid_response"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<10+1))
	if err != nil {
		return Token{}, time.Time{}, tokenTransportError(ctx, "read_response")
	}
	prefix := "aho_"
	if key.kind == "application" {
		prefix = "aho_app_"
	}
	token, ttl, err := decodeProfileToken(raw, key.scopes, prefix, key.kind == "application")
	if err != nil {
		return Token{}, time.Time{}, err
	}
	// Start before the HTTP exchange, not after it: network/processing delay
	// must never inflate the server's remaining lifetime.
	token.expiresAt = started.Add(ttl)
	if !p.now().Before(token.expiresAt) {
		return Token{}, time.Time{}, &TokenError{StatusCode: 200, Code: "expired_response"}
	}
	early := ttl/10 + time.Duration(randv2.Int64N(int64(ttl/20)+1))
	return token, token.expiresAt.Add(-early), nil
}

func tokenTransportError(ctx context.Context, code string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrToken, ctx.Err())
	}
	return &TokenError{Code: code}
}

func decodeToken(raw []byte, requested string) (Token, time.Duration, error) {
	return decodeProfileToken(raw, requested, "aho_", false)
}

func decodeProfileToken(raw []byte, requested, prefix string, application bool) (Token, time.Duration, error) {
	invalid := &TokenError{StatusCode: 200, Code: "invalid_response"}
	if len(raw) > 16<<10 {
		return Token{}, 0, invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return Token{}, 0, invalid
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		name, err := d.Token()
		key, ok := name.(string)
		if err != nil || !ok {
			return Token{}, 0, invalid
		}
		if _, exists := fields[key]; exists {
			return Token{}, 0, invalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return Token{}, 0, invalid
		}
		fields[key] = value
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return Token{}, 0, invalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Token{}, 0, invalid
	}
	// OAuth standard field names are intentional. Unknown extensions are ignored;
	// case aliases and ambiguous/missing mandatory values are not accepted.
	for key := range fields {
		switch strings.ToLower(key) {
		case "access_token", "token_type", "expires_in", "scope":
			if key != strings.ToLower(key) {
				return Token{}, 0, invalid
			}
		}
	}
	var value, kind, scope string
	var seconds int64
	for _, name := range []string{"access_token", "token_type", "expires_in"} {
		v, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return Token{}, 0, invalid
		}
	}
	if !application {
		v, exists := fields["scope"]
		if !exists || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return Token{}, 0, invalid
		}
	} else if v, exists := fields["scope"]; exists {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &scope) != nil || scope != "" {
			return Token{}, 0, invalid
		}
	}
	if json.Unmarshal(fields["access_token"], &value) != nil || !validProfileBearer(value, prefix) ||
		json.Unmarshal(fields["token_type"], &kind) != nil || kind != "Bearer" ||
		json.Unmarshal(fields["expires_in"], &seconds) != nil || seconds < 1 || seconds > 300 ||
		(fields["scope"] != nil && json.Unmarshal(fields["scope"], &scope) != nil) {
		return Token{}, 0, invalid
	}
	if !application {
		scopes := strings.Split(scope, " ")
		// Require the exact granted set, including no extra or missing permissions.
		canonical, err := normalizeScopes(scopes)
		if err != nil || canonical != requested {
			return Token{}, 0, invalid
		}
		return Token{value: value, scope: canonical}, time.Duration(seconds) * time.Second, nil
	}
	return Token{value: value}, time.Duration(seconds) * time.Second, nil
}

func validBearer(value string) bool {
	return validProfileBearer(value, "aho_")
}

func validProfileBearer(value, prefix string) bool {
	if len(value) != len(prefix)+43 || !strings.HasPrefix(value, prefix) {
		return false
	}
	entropy, err := base64.RawURLEncoding.Strict().DecodeString(value[len(prefix):])
	return err == nil && len(entropy) == 32
}
