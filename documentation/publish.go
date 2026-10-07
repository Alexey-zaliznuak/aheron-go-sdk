package docs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Publish releases one immutable package. operationID must be stable across job
// retries (e.g. pipeline-123), and unique for each provider's intended release.
// There are no automatic CAS retries: a concurrent publisher requires review.
// A receipt proves a past activation; success also checks that it is still current.
func (c *Client) Publish(ctx context.Context, p Package, operationID string) (Receipt, error) {
	return c.publishPackage(ctx, p, operationID, true, false)
}

// ActivatePackage publishes a previously uploaded package without uploading it.
// CI uses Upload before deployment, then ActivatePackage with the same artifact
// only after that exact service release is ready. Retrying uses the same stable
// operationID and verifies the receipt and current channel, just like Publish.
func (c *Client) ActivatePackage(ctx context.Context, p Package, operationID string) (Receipt, error) {
	return c.publishPackage(ctx, p, operationID, false, false)
}

// PublishCurrent publishes an integration package and atomically promotes its
// provider-wide current channel as well as the exact contract channel.
func (c *Client) PublishCurrent(ctx context.Context, p Package, operationID string) (Receipt, error) {
	return c.publishPackage(ctx, p, operationID, true, true)
}

// ActivateCurrentPackage promotes a previously uploaded integration package.
// Both channel revisions are read once; concurrent changes require reconciliation.
func (c *Client) ActivateCurrentPackage(ctx context.Context, p Package, operationID string) (Receipt, error) {
	return c.publishPackage(ctx, p, operationID, false, true)
}

func (c *Client) publishPackage(ctx context.Context, p Package, operationID string, upload, current bool) (Receipt, error) {
	p, raw, err := CanonicalPackage(p)
	if err != nil {
		return Receipt{}, err
	}
	if err := c.checkPublicationPackage(p); err != nil {
		return Receipt{}, err
	}
	if !ValidKey(operationID) || strings.Contains(operationID, "/") {
		return Receipt{}, errors.New("documentation: invalid operation ID")
	}
	if _, ok := IntegrationProviderID(p.ProviderKey); current && !ok {
		return Receipt{}, errors.New("documentation: current publication requires an integration provider")
	}
	digest := SHA256(raw)
	check := func(receipt Receipt) (Receipt, error) {
		if receipt.OperationID != operationID || receipt.Channel.ProviderKey != p.ProviderKey || receipt.Channel.ContractRevision != p.ContractRevision || receipt.Channel.PackageDigest != digest || receipt.Channel.Revision < 1 {
			return Receipt{}, errors.New("documentation: publication receipt mismatch")
		}
		if (receipt.CurrentChannel != nil) != current {
			return Receipt{}, errors.New("documentation: publication receipt current-channel mode mismatch")
		}
		catalog, err := c.Catalog(ctx, CatalogRequest{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, Locale: p.Documents[0].Locale, Limit: 1})
		if err != nil {
			return Receipt{}, fmt.Errorf("documentation: verify active publication: %w", err)
		}
		if catalog.Channel.ProviderKey != p.ProviderKey || catalog.Channel.ContractRevision != p.ContractRevision || catalog.Channel.PackageDigest != digest || catalog.Channel.Revision != receipt.Channel.Revision || catalog.SourceRevision != p.SourceRevision {
			return Receipt{}, errors.New("documentation: publication is no longer current")
		}
		if current {
			channel := receipt.CurrentChannel
			if channel.ProviderKey != p.ProviderKey || channel.ContractRevision != p.ContractRevision || channel.PackageDigest != digest || channel.Revision < 1 {
				return Receipt{}, errors.New("documentation: current publication receipt mismatch")
			}
			active, err := c.CurrentCatalog(ctx, CurrentCatalogRequest{ProviderKey: p.ProviderKey, Locale: p.Documents[0].Locale, Limit: 1})
			if err != nil {
				return Receipt{}, fmt.Errorf("documentation: verify current publication: %w", err)
			}
			if active.Channel.ProviderKey != p.ProviderKey || active.Channel.ContractRevision != p.ContractRevision || active.Channel.PackageDigest != digest || active.Channel.Revision != channel.Revision || active.SourceRevision != p.SourceRevision {
				return Receipt{}, errors.New("documentation: integration publication is no longer current")
			}
		}
		return receipt, nil
	}
	// Recover before reading a new expectedRevision. A fresh job token may be used
	// for the same checkout, without changing the original activation request.
	if receipt, err := c.Receipt(ctx, operationID); err == nil {
		return check(receipt)
	} else if !apiStatus(err, http.StatusNotFound) {
		return Receipt{}, fmt.Errorf("documentation: recover publication: %w", err)
	}
	if upload {
		if _, err := c.Upload(ctx, p); err != nil {
			return Receipt{}, fmt.Errorf("documentation: upload package: %w", err)
		}
	}
	var expected int64
	catalog, err := c.Catalog(ctx, CatalogRequest{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, Locale: p.Documents[0].Locale, Limit: 1})
	if err == nil {
		if catalog.Channel.ProviderKey != p.ProviderKey || catalog.Channel.ContractRevision != p.ContractRevision || catalog.Channel.Revision < 1 || !ValidDigest(catalog.Channel.PackageDigest) {
			return Receipt{}, errors.New("documentation: channel identity mismatch")
		}
		expected = catalog.Channel.Revision
	} else if !apiStatus(err, http.StatusNotFound) {
		return Receipt{}, fmt.Errorf("documentation: read publication channel: %w", err)
	}
	request := ActivateRequest{PackageDigest: digest, ContractRevision: p.ContractRevision, ExpectedRevision: expected, OperationID: operationID}
	if current {
		var revision int64
		catalog, err := c.CurrentCatalog(ctx, CurrentCatalogRequest{ProviderKey: p.ProviderKey, Locale: p.Documents[0].Locale, Limit: 1})
		if err == nil {
			if catalog.Channel.ProviderKey != p.ProviderKey || !ValidKey(catalog.Channel.ContractRevision) || catalog.Channel.Revision < 1 || !ValidDigest(catalog.Channel.PackageDigest) {
				return Receipt{}, errors.New("documentation: current channel identity mismatch")
			}
			revision = catalog.Channel.Revision
		} else if !apiStatus(err, http.StatusNotFound) {
			return Receipt{}, fmt.Errorf("documentation: read current publication channel: %w", err)
		}
		request.ExpectedCurrentRevision = &revision
	}
	receipt, err := c.Activate(ctx, request)
	if err == nil {
		return check(receipt)
	}
	// The server may have committed before a network failure or before another
	// retry of this operation returned 409. One bounded receipt read resolves that
	// ambiguity. Never silently publish against a freshly read channel revision.
	if recovered, recoveryErr := c.Receipt(ctx, operationID); recoveryErr == nil {
		return check(recovered)
	}
	return Receipt{}, fmt.Errorf("documentation: activation unconfirmed; retry with the same operation ID: %w", err)
}

func apiStatus(err error, status int) bool {
	var api *APIError
	return errors.As(err, &api) && api.StatusCode == status
}
