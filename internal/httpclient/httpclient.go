// Package httpclient is the SDK's low-level transport. It wraps a resty client
// with bounded retries, structured logging and typed error mapping, and exposes
// a single Do method. It is intentionally auth-agnostic: callers pass a fully
// built request (pre-serialized body and all headers, including any signature or
// bearer headers), so this layer never touches credentials.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"

	"github.com/go-resty/resty/v2"
)

// Defaults applied when a Config field is left zero.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultRetryCount   = 2
	DefaultRetryWaitMin = 500 * time.Millisecond
	DefaultRetryWaitMax = 5 * time.Second
)

// retryableStatuses are transient upstream failures where the request most
// likely never reached application logic, so a retry is safe.
var retryableStatuses = map[int]bool{502: true, 503: true, 504: true}

// ctxKey is the private type for values this package stashes on a request
// context (kept unexported so no other package can collide).
type ctxKey int

const retryableKey ctxKey = iota

// Request is a fully prepared HTTP call. Body must already be serialized (the
// transport does not marshal); Headers must already include Content-Type when a
// body is present, plus any auth/signature headers. Idempotent marks the call as
// safe to retry on a transient failure — set it only for GETs and for callbacks
// the platform deduplicates (resolve, trigger activation).
type Request struct {
	Method     string
	Path       string
	Query      map[string]string
	Headers    map[string]string
	Body       []byte
	Idempotent bool
}

// Response is the decoded transport result: the HTTP status and the raw body.
type Response struct {
	Status int
	Body   []byte
}

// APIError is returned for any non-2xx response. Message is the best-effort
// human-readable error extracted from the JSON body ({"error": "..."}).
type APIError struct {
	Method  string
	URL     string
	Status  int
	Message string
	Body    []byte
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s %s: %d %s", e.Method, e.URL, e.Status, e.Message)
	}
	return fmt.Sprintf("%s %s: %d", e.Method, e.URL, e.Status)
}

// Config configures the transport. Zero fields fall back to the Default*
// constants; a nil Logger falls back to a no-op.
type Config struct {
	BaseURL      string
	Timeout      time.Duration
	RetryCount   int
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
	Logger       logx.Logger
}

// Client is a transport bound to a single base URL.
type Client struct {
	r   *resty.Client
	log logx.Logger
}

// New builds a Client from cfg, wiring resty's retry policy so only idempotent
// requests (Request.Idempotent) are retried, and only on network errors or a
// retryable status.
func New(cfg Config) *Client {
	log := cfg.Logger
	if log == nil {
		log = logx.Nop()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	retryCount := cfg.RetryCount
	if retryCount < 0 {
		retryCount = 0
	} else if retryCount == 0 {
		retryCount = DefaultRetryCount
	}
	waitMin := cfg.RetryWaitMin
	if waitMin <= 0 {
		waitMin = DefaultRetryWaitMin
	}
	waitMax := cfg.RetryWaitMax
	if waitMax <= 0 {
		waitMax = DefaultRetryWaitMax
	}

	r := resty.New().
		SetBaseURL(strings.TrimRight(cfg.BaseURL, "/")).
		SetTimeout(timeout).
		SetRetryCount(retryCount).
		SetRetryWaitTime(waitMin).
		SetRetryMaxWaitTime(waitMax).
		AddRetryCondition(func(resp *resty.Response, err error) bool {
			// Retry only requests the caller marked idempotent.
			if resp == nil || resp.Request == nil || !isIdempotent(resp.Request.Context()) {
				return false
			}
			if err != nil {
				return true // network/transport error
			}
			return retryableStatuses[resp.StatusCode()]
		})

	return &Client{r: r, log: log}
}

// Do executes req and returns the decoded response. A non-2xx status yields a
// non-nil *Response together with a *APIError so callers can inspect both.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if req.Idempotent {
		ctx = context.WithValue(ctx, retryableKey, true)
	}

	rr := c.r.R().SetContext(ctx)
	for k, v := range req.Headers {
		rr.SetHeader(k, v)
	}
	for k, v := range req.Query {
		rr.SetQueryParam(k, v)
	}
	if req.Body != nil {
		rr.SetHeader("Content-Type", "application/json")
		rr.SetBody(req.Body)
	}

	c.log.Debug("integration sdk request",
		logx.F("method", req.Method),
		logx.F("path", req.Path),
	)

	resp, err := rr.Execute(strings.ToUpper(req.Method), req.Path)
	if err != nil {
		c.log.Error("integration sdk transport error",
			logx.F("method", req.Method),
			logx.F("path", req.Path),
			logx.F("error", err.Error()),
		)
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.Path, err)
	}

	out := &Response{Status: resp.StatusCode(), Body: resp.Body()}
	if out.Status < 200 || out.Status >= 300 {
		apiErr := &APIError{
			Method:  strings.ToUpper(req.Method),
			URL:     resp.Request.URL,
			Status:  out.Status,
			Message: extractMessage(out.Body),
			Body:    out.Body,
		}
		c.log.Warn("integration sdk api error",
			logx.F("method", apiErr.Method),
			logx.F("status", apiErr.Status),
			logx.F("message", apiErr.Message),
		)
		return out, apiErr
	}
	return out, nil
}

func isIdempotent(ctx context.Context) bool {
	v, _ := ctx.Value(retryableKey).(bool)
	return v
}

// extractMessage pulls a human-readable message out of a JSON error body,
// tolerating either {"error":"..."} or {"message":"..."}. It falls back to the
// trimmed raw body when the shape is unknown.
func extractMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Error != "" {
			return envelope.Error
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	return strings.TrimSpace(string(body))
}
