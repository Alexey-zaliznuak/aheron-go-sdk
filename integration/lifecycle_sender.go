package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

var ErrLifecycleDelivery = errors.New("integration: lifecycle delivery failed")

// LifecycleDeliveryError contains safe classifications only: never request
// bodies, credentials, destination URLs or arbitrary response text.
type LifecycleDeliveryError struct {
	StatusCode int
	Code       string
}

func (e *LifecycleDeliveryError) Error() string {
	return fmt.Sprintf("%s: %s (HTTP %d)", ErrLifecycleDelivery, e.Code, e.StatusCode)
}
func (e *LifecycleDeliveryError) Unwrap() error { return ErrLifecycleDelivery }

// LifecycleSender is the platform side of the protocol. It performs ONE attempt;
// backend's durable operation owns retry/backoff and the immutable message.
// Destination must come from the installation's approved lifecycle contract,
// never from an untrusted request or a newly changed catalog during a retry.
type LifecycleSender struct {
	key       ed25519.PrivateKey
	keyID     string
	client    *http.Client
	allowHTTP bool
}

type LifecycleSenderConfig struct {
	PrivateKey ed25519.PrivateKey
	KeyID      string
	HTTPClient *http.Client
	// AllowHTTP is for explicit local development/private transport deployments.
	// Production callbacks carrying a project credential require HTTPS.
	AllowHTTP bool
}

func NewLifecycleSender(cfg LifecycleSenderConfig) (*LifecycleSender, error) {
	if len(cfg.PrivateKey) != ed25519.PrivateKeySize || len(cfg.KeyID) == 0 || len(cfg.KeyID) > 128 || strings.ContainsAny(cfg.KeyID, "\r\n\x00") || strings.TrimSpace(cfg.KeyID) != cfg.KeyID {
		return nil, ErrLifecycleInvalid
	}
	for _, c := range cfg.KeyID {
		if c < 33 || c > 126 {
			return nil, ErrLifecycleInvalid
		}
	}
	if !bytes.Equal(cfg.PrivateKey, ed25519.NewKeyFromSeed(cfg.PrivateKey.Seed())) {
		return nil, ErrLifecycleInvalid
	}
	client := http.Client{Timeout: 10 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	if client.Timeout <= 0 {
		client.Timeout = 10 * time.Second
	}
	// Copy rather than mutate the caller's HTTP client. Redirects can change
	// both recipient and method and must never carry a signed credential body.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &LifecycleSender{key: append(ed25519.PrivateKey(nil), cfg.PrivateKey...), keyID: cfg.KeyID, client: &client, allowHTTP: cfg.AllowHTTP}, nil
}

func (s *LifecycleSender) Deliver(ctx context.Context, destination string, request LifecycleRequest) (LifecycleReceipt, error) {
	if s == nil {
		return LifecycleReceipt{}, ErrLifecycleDelivery
	}
	if err := request.Validate(); err != nil {
		return LifecycleReceipt{}, err
	}
	u, err := url.Parse(destination)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && !(s.allowHTTP && u.Scheme == "http")) {
		return LifecycleReceipt{}, ErrLifecycleInvalid
	}
	body, err := json.Marshal(request)
	if err != nil {
		return LifecycleReceipt{}, ErrLifecycleInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(body))
	if err != nil {
		return LifecycleReceipt{}, ErrLifecycleInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	ts := sign.FormatTimestamp(time.Now())
	req.Header.Set(sign.HeaderPlatformTimestamp, ts)
	req.Header.Set(sign.HeaderPlatformSignature, sign.SignDomain(s.key, LifecycleProtocol, ts, body))
	req.Header.Set(sign.HeaderPlatformKeyID, s.keyID)
	response, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return LifecycleReceipt{}, fmt.Errorf("%w: %w", ErrLifecycleDelivery, ctx.Err())
		}
		return LifecycleReceipt{}, &LifecycleDeliveryError{Code: "transport"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return LifecycleReceipt{}, &LifecycleDeliveryError{StatusCode: response.StatusCode, Code: "http_status"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxLifecycleBody+1))
	if err != nil {
		return LifecycleReceipt{}, &LifecycleDeliveryError{StatusCode: response.StatusCode, Code: "read_receipt"}
	}
	var receipt LifecycleReceipt
	if decodeLifecycleObject(raw, &receipt, lifecycleReceiptFields) != nil || receipt.ValidateFor(request) != nil {
		return LifecycleReceipt{}, &LifecycleDeliveryError{StatusCode: response.StatusCode, Code: "invalid_receipt"}
	}
	return receipt, nil
}
