package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// LinksClient owns link-service URLs, types and authentication. Prefer a stable creation key per outgoing message/action.
type LinksClient struct {
	http                *httpclient.Client
	baseURL, id, apiKey string
	signer              *sign.Signer
}

func (c *LinksClient) WithAPIKey(key string) *LinksClient { out := *c; out.apiKey = key; return &out }

type LinkCallback struct {
	EndpointKey string          `json:"endpointKey"`
	Data        json.RawMessage `json:"data,omitempty"`
}
type CreateLinkRequest struct {
	TargetURL string        `json:"targetUrl"`
	ExpiresAt *time.Time    `json:"expiresAt,omitempty"`
	Callback  *LinkCallback `json:"callback,omitempty"`
}
type Link struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"projectId"`
	IntegrationID string        `json:"integrationId,omitempty"`
	TargetURL     string        `json:"targetUrl"`
	ShortURL      string        `json:"shortUrl"`
	Callback      *LinkCallback `json:"callback,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	ExpiresAt     *time.Time    `json:"expiresAt,omitempty"`
	DisabledAt    *time.Time    `json:"disabledAt,omitempty"`
}
type LinkEndpointRequest struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}
type LinkEndpoint struct {
	IntegrationID string `json:"integrationId"`
	Key           string `json:"key"`
	URL           string `json:"url"`
	Enabled       bool   `json:"enabled"`
	Version       int64  `json:"version"`
}
type LinkDelivery struct {
	EventID       string     `json:"eventId"`
	LinkID        string     `json:"linkId"`
	ProjectID     string     `json:"projectId"`
	IntegrationID string     `json:"integrationId"`
	EndpointKey   string     `json:"endpointKey"`
	OccurredAt    time.Time  `json:"occurredAt"`
	Status        string     `json:"status"`
	Attempts      int32      `json:"attempts"`
	NextAttemptAt time.Time  `json:"nextAttemptAt"`
	LastErrorCode string     `json:"lastErrorCode,omitempty"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}
type LinkPage struct {
	Items      []Link `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type LinkDeliveryPage struct {
	Items      []LinkDelivery `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

var linkIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{16}$`)
var linkUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func linkPath(project, id string) (string, error) {
	if !linkUUIDPattern.MatchString(project) {
		return "", fmt.Errorf("links: invalid projectId")
	}
	p := "/projects/" + project + "/links"
	if id != "" {
		if !linkIDPattern.MatchString(id) {
			return "", fmt.Errorf("links: invalid 16-character id")
		}
		p += "/" + id
	}
	return p, nil
}
func (c *LinksClient) call(ctx context.Context, method, path, key string, in, out any, idempotent bool) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	headers := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	if c.signer != nil && c.id != "" {
		base, e := url.Parse(strings.TrimRight(c.baseURL, "/"))
		if e != nil {
			return e
		}
		full, e := url.Parse(strings.TrimRight(c.baseURL, "/") + path)
		if e != nil {
			return e
		}
		if base.RawQuery != "" || base.Fragment != "" {
			return fmt.Errorf("links: base URL must not contain query/fragment")
		}
		prefix := fmt.Sprintf("links-request-v1\n%s\n%s\n%s\n", method, full.RequestURI(), key)
		ts, sig := c.signer.Sign(append([]byte(prefix), body...), time.Now())
		headers[sign.HeaderIntegrationID] = c.id
		headers[sign.HeaderIntegrationTimestamp] = ts
		headers[sign.HeaderIntegrationSignature] = sig
	} else {
		if c.apiKey == "" {
			return errNoAPIKey
		}
		headers["Authorization"] = "Bearer " + c.apiKey
	}
	resp, err := c.http.Do(ctx, httpclient.Request{Method: method, Path: path, Headers: headers, Body: body, Idempotent: idempotent})
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}
func (c *LinksClient) Create(ctx context.Context, project string, in CreateLinkRequest, idempotencyKey string) (Link, error) {
	var out Link
	p, e := linkPath(project, "")
	if e != nil {
		return out, e
	}
	if len(idempotencyKey) == 0 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n") {
		return out, fmt.Errorf("links: stable Idempotency-Key (1-128 bytes) required")
	}
	e = c.call(ctx, http.MethodPost, p, idempotencyKey, in, &out, true)
	return out, e
}
func (c *LinksClient) Get(ctx context.Context, project, id string) (Link, error) {
	var out Link
	p, e := linkPath(project, id)
	if e != nil {
		return out, e
	}
	e = c.call(ctx, "GET", p, "", nil, &out, true)
	return out, e
}
func (c *LinksClient) Disable(ctx context.Context, project, id string) error {
	p, e := linkPath(project, id)
	if e != nil {
		return e
	}
	return c.call(ctx, "DELETE", p, "", nil, nil, true)
}
func (c *LinksClient) RegisterCallback(ctx context.Context, key string, in LinkEndpointRequest) (LinkEndpoint, error) {
	var out LinkEndpoint
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(key) {
		return out, fmt.Errorf("links: invalid endpoint key")
	}
	if c.signer == nil || c.id == "" {
		return out, errNoSigner
	}
	e := c.call(ctx, "PUT", "/integrations/self/link-callbacks/"+key, "", in, &out, true)
	return out, e
}
func (c *LinksClient) List(ctx context.Context, project, cursor string, limit int) (LinkPage, error) {
	var out LinkPage
	p, e := linkPath(project, "")
	if e != nil {
		return out, e
	}
	p += fmt.Sprintf("?cursor=%s&limit=%d", url.QueryEscape(cursor), limit)
	e = c.call(ctx, "GET", p, "", nil, &out, true)
	return out, e
}
func (c *LinksClient) Deliveries(ctx context.Context, project, id, cursor string, limit int) (LinkDeliveryPage, error) {
	var out LinkDeliveryPage
	p, e := linkPath(project, id)
	if e != nil {
		return out, e
	}
	p += fmt.Sprintf("/deliveries?cursor=%s&limit=%d", url.QueryEscape(cursor), limit)
	e = c.call(ctx, "GET", p, "", nil, &out, true)
	return out, e
}

// Replay preserves eventId. It is not automatically retried: the operation changes failed -> pending.
func (c *LinksClient) Replay(ctx context.Context, project, id, eventID string) error {
	p, e := linkPath(project, id)
	if e != nil {
		return e
	}
	if !linkUUIDPattern.MatchString(eventID) {
		return fmt.Errorf("links: invalid eventId")
	}
	return c.call(ctx, "POST", p+"/deliveries/"+eventID+"/replay", "", nil, nil, false)
}
