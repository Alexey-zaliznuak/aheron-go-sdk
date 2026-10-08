package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL    string // API prefix, e.g. https://docs.aheron.pro/api/documentation
	HTTPClient *http.Client
	// DisableTimeout removes the client timeout; context and credential expiry still apply.
	DisableTimeout    bool
	Timeout           time.Duration
	AllowLoopbackHTTP bool
	PublisherToken    func(context.Context) (string, error)
	// Publication supplies release metadata for personal API-key publishing.
	// OIDC callers omit it; their metadata comes from the verified token.
	Publication *PublicationContext
}

// PublicationContext identifies a release, not its authority. The server
// authenticates the key and checks the owner's current rights separately.
// ReleaseSequence must increase within a provider, and stay unchanged on retry.
type PublicationContext struct {
	ProviderKey     string
	SourceRevision  string
	ReleaseSequence int64
}

type Client struct {
	baseURL     string
	http        *http.Client
	timeout     time.Duration
	token       func(context.Context) (string, error)
	publication *PublicationContext
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("documentation: invalid base URL")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(cfg.AllowLoopbackHTTP && loopback && u.Scheme == "http") {
		return nil, errors.New("documentation: HTTPS required")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("documentation: invalid timeout")
	}
	if cfg.DisableTimeout {
		cfg.Timeout = 0
	} else if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	hc := http.Client{}
	if cfg.HTTPClient != nil {
		hc = *cfg.HTTPClient
	}
	if cfg.DisableTimeout {
		hc.Timeout = 0
	}
	// Never forward publisher credentials through a redirect, including same-host ones.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var publication *PublicationContext
	if cfg.Publication != nil {
		p := *cfg.Publication
		if !ValidKey(p.ProviderKey) || !ValidSourceRevision(p.SourceRevision) || p.ReleaseSequence < 1 {
			return nil, errors.New("documentation: valid publication provider, source revision and positive release sequence required")
		}
		publication = &p
	}
	return &Client{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: &hc, timeout: cfg.Timeout, token: cfg.PublisherToken, publication: publication}, nil
}

type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("documentation: HTTP %d (%s)", e.StatusCode, e.Code)
}

func (c *Client) request(ctx context.Context, method, path string, in, out any, publish bool) error {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	if len(body) > MaxPackageBytes {
		return errors.New("documentation: request too large")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if publish {
		if c.token == nil {
			return errors.New("documentation: publisher token provider required")
		}
		token, err := c.token(ctx)
		if err != nil {
			return err
		}
		if token == "" {
			return errors.New("documentation: empty publisher token")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if p := c.publication; p != nil {
			req.Header.Set("X-Documentation-Provider", p.ProviderKey)
			req.Header.Set("X-Documentation-Source-Revision", p.SourceRevision)
			req.Header.Set("X-Documentation-Release-Sequence", strconv.FormatInt(p.ReleaseSequence, 10))
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseLimit := MaxResponseBytes
	if path == "/public/knowledge/library/read" {
		// A manual article contains up to 2 MiB of Markdown; JSON may escape each byte.
		responseLimit = 6*(2<<20) + (64 << 10)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(responseLimit)+1))
	if err != nil {
		return err
	}
	if len(raw) > responseLimit {
		return errors.New("documentation: response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &problem)
		return &APIError{StatusCode: resp.StatusCode, Code: problem.Error.Code}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("documentation: decode response: %w", err)
	}
	return nil
}

func (c *Client) Catalog(ctx context.Context, req CatalogRequest) (Catalog, error) {
	var out Catalog
	err := c.request(ctx, "POST", "/public/knowledge/catalog", req, &out, false)
	return out, err
}
func (c *Client) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	var out ReadResult
	err := c.request(ctx, "POST", "/public/knowledge/read", req, &out, false)
	if err == nil && (out.Document.Ref != req.Ref || out.Audience != req.Audience || out.ContentSHA256 != SHA256([]byte(out.Markdown))) {
		err = errors.New("documentation: response identity or content hash mismatch")
	}
	return out, err
}
func (c *Client) Upload(ctx context.Context, p Package) (UploadResult, error) {
	canonical, raw, err := CanonicalPackage(p)
	if err != nil {
		return UploadResult{}, err
	}
	if err := c.checkPublicationPackage(canonical); err != nil {
		return UploadResult{}, err
	}
	var out UploadResult
	err = c.request(ctx, "POST", "/publishing/packages", canonical, &out, true)
	if err == nil && (out.ProviderKey != canonical.ProviderKey || out.ContractRevision != canonical.ContractRevision || out.PackageDigest != SHA256(raw)) {
		err = errors.New("documentation: upload receipt mismatch")
	}
	return out, err
}

func (c *Client) checkPublicationPackage(p Package) error {
	if c.publication != nil && (c.publication.ProviderKey != p.ProviderKey || c.publication.SourceRevision != p.SourceRevision) {
		return errors.New("documentation: package differs from publication context")
	}
	return nil
}
func (c *Client) Activate(ctx context.Context, req ActivateRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, "POST", "/publishing/activate", req, &out, true)
	return out, err
}

func (c *Client) CurrentCatalog(ctx context.Context, req CurrentCatalogRequest) (Catalog, error) {
	var out Catalog
	err := c.request(ctx, "POST", "/public/knowledge/current/catalog", req, &out, false)
	return out, err
}
func (c *Client) Receipt(ctx context.Context, operationID string) (Receipt, error) {
	if !ValidKey(operationID) || strings.Contains(operationID, "/") {
		return Receipt{}, errors.New("documentation: invalid operation ID")
	}
	var out Receipt
	err := c.request(ctx, "GET", "/publishing/receipts/"+url.PathEscape(operationID), nil, &out, true)
	return out, err
}
