package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// selfSyncPath is relative to the configured CatalogURL, which already carries
// the gateway's "/api" prefix. There is no integration id in the path on
// purpose: the platform takes it from the verified signature, so an integration
// cannot address anyone's catalog entry but its own.
const selfSyncPath = "/integrations/self/sync"

// CatalogClient publishes the integration's own catalog declaration. The call is
// signed with the integration's private key.
type CatalogClient struct {
	http          *httpclient.Client
	id            string
	signer        *sign.Signer
	publicBaseURL string
	log           logx.Logger
}

// SyncResult reports what the platform did with the manifest.
//
// Changed is false when the manifest already matched the published version, in
// which case nothing was written — this is the normal outcome of a restart.
//
// Published is false when the platform prepared a draft but could not publish
// it, and Reason says why. The only case today is a subflow block whose
// sub-scheme has not been authored yet: the draft is waiting for a human in the
// platform UI. It is not an error — the integration keeps running and should log
// it as a warning.
type SyncResult struct {
	Changed   bool   `json:"changed"`
	Version   int    `json:"version"`
	Published bool   `json:"published"`
	Reason    string `json:"reason,omitempty"`
}

// Sync declares the integration's blocks and endpoint contract to the platform
// catalog. It is the whole desired state: blocks the manifest does not list are
// removed (subject to Manifest.Retired) and endpoints it does not declare are
// cleared.
//
// The call is idempotent. When the manifest matches the published version the
// platform writes nothing and returns Changed=false, so calling it on every
// start of every replica is safe and converges: replicas racing to publish the
// same manifest end up with a single new version.
//
// It returns an error for a manifest that cannot be resolved (checked locally,
// before signing) and for a platform rejection — most notably a block that
// disappeared from the manifest without being listed in Manifest.Retired.
func (c *CatalogClient) Sync(ctx context.Context, m Manifest) (SyncResult, error) {
	payload, err := m.resolve(c.publicBaseURL)
	if err != nil {
		return SyncResult{}, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return SyncResult{}, fmt.Errorf("integration: marshal manifest: %w", err)
	}

	req, err := buildSignedRequest(c.signer, c.id, http.MethodPost, selfSyncPath, nil, body, true)
	if err != nil {
		return SyncResult{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return SyncResult{}, err
	}

	var out SyncResult
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return SyncResult{}, fmt.Errorf("integration: decode sync response: %w", err)
		}
	}
	return out, nil
}

// Startup sync pacing. The initial jitter spreads the replicas of one
// integration, which all start within a second of each other during a rolling
// update: without it they would race to publish the same manifest and all but one
// would do the extra round trip of losing that race.
const (
	syncStartJitter  = 3 * time.Second
	syncAttempts     = 5
	syncBackoffStart = 2 * time.Second
	syncBackoffMax   = 30 * time.Second
)

// StartSync publishes the manifest in the background and logs the outcome. Call
// it in a goroutine from the service's start-up path:
//
//	go client.Catalog.StartSync(ctx, catalog.Manifest())
//
// It never returns an error and must never gate readiness. A catalog that is
// unreachable is not a reason to stop serving the actions and webhooks the
// integration already has published declarations for — the worst case is that the
// new declarations land on the next restart.
//
// It waits out a short jitter, then retries a handful of times with backoff,
// stopping as soon as the sync succeeds or ctx is cancelled.
func (c *CatalogClient) StartSync(ctx context.Context, m Manifest) {
	if !sleepCtx(ctx, rand.N(syncStartJitter)) {
		return
	}

	backoff := syncBackoffStart
	for attempt := 1; attempt <= syncAttempts; attempt++ {
		result, err := c.Sync(ctx, m)
		if err == nil {
			switch {
			case !result.Changed:
				c.log.Info("integration catalog already up to date",
					logx.F("version", result.Version))
			case result.Published:
				c.log.Info("integration catalog published a new version",
					logx.F("version", result.Version))
			default:
				// A draft that needs a human (a subflow block with no sub-scheme).
				c.log.Warn("integration catalog version prepared but not published",
					logx.F("version", result.Version),
					logx.F("reason", result.Reason))
			}
			return
		}
		if ctx.Err() != nil {
			return
		}

		if attempt == syncAttempts {
			c.log.Error("integration catalog sync failed, giving up until the next start",
				logx.F("attempts", attempt),
				logx.F("error", err.Error()))
			return
		}
		c.log.Warn("integration catalog sync failed, retrying",
			logx.F("attempt", attempt),
			logx.F("retryIn", backoff.String()),
			logx.F("error", err.Error()))

		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > syncBackoffMax {
			backoff = syncBackoffMax
		}
	}
}

// sleepCtx waits for d, reporting false when ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
