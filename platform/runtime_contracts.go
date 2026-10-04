package platform

import (
	"context"
	"net/http"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

// RuntimeContracts is an uncached observation of responding service instances,
// not a guarantee about every replica or every Kafka worker. An unavailable
// provider must not be satisfied by a build-time constant or a latest document.
type RuntimeContracts struct {
	Contracts            []docs.Contract `json:"contracts"`
	UnavailableProviders []string        `json:"unavailableProviders"`
}

func (s *SchemesClient) RuntimeContracts(ctx context.Context, projectID string) (RuntimeContracts, error) {
	if !resourceID.MatchString(projectID) {
		return RuntimeContracts{}, ErrInvalidInput
	}
	var out RuntimeContracts
	err := s.client.requestJSON(ctx, "observe runtime contracts", http.MethodGet, "/projects/"+projectID+"/runtime-contracts", nil, nil, &out)
	if err != nil {
		return RuntimeContracts{}, err
	}
	if out.Contracts == nil || out.UnavailableProviders == nil || len(out.Contracts)+len(out.UnavailableProviders) > 32 {
		return RuntimeContracts{}, ErrResponse
	}
	seen := map[string]bool{}
	check := func(provider string) bool {
		if !docs.ValidKey(provider) || seen[provider] {
			return false
		}
		seen[provider] = true
		return true
	}
	for _, c := range out.Contracts {
		if !check(c.ProviderKey) || !docs.ValidKey(c.ContractRevision) {
			return RuntimeContracts{}, ErrResponse
		}
	}
	for _, p := range out.UnavailableProviders {
		if !check(p) {
			return RuntimeContracts{}, ErrResponse
		}
	}
	if !seen["platform/execution"] || !seen["platform/code-execution"] {
		return RuntimeContracts{}, ErrResponse
	}
	return out, nil
}
