package integration

import (
	"context"
	"errors"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

var errAnalyticsDefinitionResponse = errors.New("invalid analytics definition response")

// Analytics definitions are ordinary project resources. Caller-specific
// associations and business defaults belong to the calling integration.
func (c *AnalyticsClient) definitionPath(project, experiment string, kind platform.AnalyticsDefinitionKind, id string) (string, error) {
	if c == nil || c.definitionsRead == nil || project != c.projectID || !integrationOAuthUUID(project) || !integrationOAuthUUID(id) {
		return "", integrationoauth.ErrRequest
	}
	p := "/clients/projects/" + project + "/experiments/"
	if kind == platform.AnalyticsFolders {
		if experiment != "" {
			return "", integrationoauth.ErrRequest
		}
		return p + id, nil
	}
	if !integrationOAuthUUID(experiment) || (kind != platform.AnalyticsMetrics && kind != platform.AnalyticsAudiences && kind != platform.AnalyticsPartitions) {
		return "", integrationoauth.ErrRequest
	}
	return p + experiment + "/" + string(kind) + "/" + id, nil
}

func (c *AnalyticsClient) GetDefinition(ctx context.Context, project, experiment string, kind platform.AnalyticsDefinitionKind, id string) (platform.AnalyticsDefinition, error) {
	var out platform.AnalyticsDefinition
	p, err := c.definitionPath(project, experiment, kind, id)
	if err != nil {
		return out, err
	}
	err = callResourceJSON(ctx, c.definitionsRead, c.baseURL, http.MethodGet, p, nil, &out, true, http.StatusOK)
	if err == nil && (out.ID != id || out.Name == "" || out.CreatedAt.IsZero()) {
		err = errAnalyticsDefinitionResponse
	}
	return out, err
}

// CreateDefinition is idempotent for the same ID and definition. It never
// overwrites an existing definition or retries a write automatically.
func (c *AnalyticsClient) CreateDefinition(ctx context.Context, project, experiment string, kind platform.AnalyticsDefinitionKind, id string, in platform.AnalyticsDefinitionInput) (platform.AnalyticsDefinition, error) {
	var out platform.AnalyticsDefinition
	p, err := c.definitionPath(project, experiment, kind, id)
	if err != nil {
		return out, err
	}
	err = callResourceJSON(ctx, c.definitionsWrite, c.baseURL, http.MethodPut, p, in, &out, true, http.StatusOK)
	if err == nil && (out.ID != id || out.Name == "" || out.CreatedAt.IsZero()) {
		err = errAnalyticsDefinitionResponse
	}
	return out, err
}
