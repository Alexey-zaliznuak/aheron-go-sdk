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
	p, raw, err := CanonicalPackage(p)
	if err != nil {
		return Receipt{}, err
	}
	if !ValidKey(operationID) || strings.Contains(operationID, "/") {
		return Receipt{}, errors.New("documentation: invalid operation ID")
	}
	digest := SHA256(raw)
	check := func(receipt Receipt) (Receipt, error) {
		if receipt.OperationID != operationID || receipt.Channel.ProviderKey != p.ProviderKey || receipt.Channel.ContractRevision != p.ContractRevision || receipt.Channel.PackageDigest != digest || receipt.Channel.Revision < 1 {
			return Receipt{}, errors.New("documentation: publication receipt mismatch")
		}
		catalog, err := c.Catalog(ctx, CatalogRequest{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, Locale: p.Documents[0].Locale, Limit: 1})
		if err != nil {
			return Receipt{}, fmt.Errorf("documentation: verify active publication: %w", err)
		}
		if catalog.Channel.ProviderKey != p.ProviderKey || catalog.Channel.ContractRevision != p.ContractRevision || catalog.Channel.PackageDigest != digest || catalog.Channel.Revision != receipt.Channel.Revision || catalog.SourceRevision != p.SourceRevision {
			return Receipt{}, errors.New("documentation: publication is no longer current")
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
	if _, err := c.Upload(ctx, p); err != nil {
		return Receipt{}, fmt.Errorf("documentation: upload package: %w", err)
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
	receipt, err := c.Activate(ctx, ActivateRequest{PackageDigest: digest, ContractRevision: p.ContractRevision, ExpectedRevision: expected, OperationID: operationID})
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
