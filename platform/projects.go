package platform

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var resourceID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Project matches backend ProjectResponse, including its nullable fields.
// Settings/Metadata may contain application data; tool adapters should select
// the fields needed by the model instead of automatically exposing everything.
type Project struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"ownerUserId"`
	Name        string          `json:"name"`
	Slug        *string         `json:"slug,omitempty"`
	Description *string         `json:"description,omitempty"`
	Settings    json.RawMessage `json:"settings"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type ProjectsClient struct{ client *Client }

// List returns projects visible to the user. The current backend contract has
// no pagination; oversized responses fail explicitly instead of truncating.
func (p *ProjectsClient) List(ctx context.Context) ([]Project, error) {
	var result []Project
	if err := p.client.get(ctx, "list projects", "/projects", &result); err != nil {
		return nil, err
	}
	for _, project := range result {
		if !resourceID.MatchString(project.ID) {
			return nil, ErrResponse
		}
	}
	return result, nil
}

func (p *ProjectsClient) Get(ctx context.Context, projectID string) (Project, error) {
	if !resourceID.MatchString(projectID) {
		return Project{}, ErrInvalidInput
	}
	var result Project
	if err := p.client.get(ctx, "get project", "/projects/"+projectID, &result); err != nil {
		return Project{}, err
	}
	if !strings.EqualFold(result.ID, projectID) {
		return Project{}, ErrResponse
	}
	return result, nil
}
