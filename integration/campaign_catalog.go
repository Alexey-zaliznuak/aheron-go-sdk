package integration

import (
	"context"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type CampaignCatalogOptions struct {
	Cursor, Name, Status string
	Limit                int
}
type CampaignSummary struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	ActivationKey string    `json:"activationKey"`
	Revision      string    `json:"revision"`
	CreatedAt     time.Time `json:"createdAt"`
}
type CampaignCatalogPage struct {
	Items        []CampaignSummary `json:"items"`
	NextCursor   string            `json:"nextCursor,omitempty"`
	ScannedCount int               `json:"scannedCount"`
}

// Catalog reads one bounded page. Continue through empty filtered pages while
// NextCursor is nonempty, keeping filters unchanged. UUID order is not chronology.
func (c *CampaignClient) Catalog(ctx context.Context, p string, in CampaignCatalogOptions) (CampaignCatalogPage, error) {
	path, err := c.projectPath(p)
	if err != nil {
		return CampaignCatalogPage{}, err
	}
	if in.Limit < 0 || in.Limit > 100 || (in.Cursor != "" && !integrationOAuthUUID(in.Cursor)) {
		return CampaignCatalogPage{}, integrationoauth.ErrRequest
	}
	q := url.Values{}
	if in.Cursor != "" {
		q.Set("cursor", in.Cursor)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	if in.Name != "" {
		q.Set("name", in.Name)
	}
	if in.Status != "" {
		q.Set("status", in.Status)
	}
	var out CampaignCatalogPage
	err = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, path+"/page?"+q.Encode(), nil, &out, true, 200)
	if err == nil {
		if out.Items == nil || len(out.Items) > 100 || out.ScannedCount < 0 || out.ScannedCount > 100 || (out.NextCursor != "" && !integrationOAuthUUID(out.NextCursor)) {
			return CampaignCatalogPage{}, errCampaignResponse
		}
		for _, v := range out.Items {
			if v.ProjectID != p || !integrationOAuthUUID(v.ID) {
				return CampaignCatalogPage{}, errCampaignResponse
			}
		}
	}
	return out, err
}
