package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
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
