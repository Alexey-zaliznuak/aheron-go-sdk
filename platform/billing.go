package platform

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"net/url"
)

type BillingClient struct{ client *Client }

type BillingQuota struct {
	Description        string  `json:"description,omitempty"`
	ID                 string  `json:"id"`
	IntegrationID      *string `json:"integrationId"`
	SystemName         string  `json:"systemName"`
	Name               string  `json:"name"`
	UnitName           string  `json:"unitName"`
	BillingStep        int64   `json:"billingStep"`
	PurchasePriceMinor *int64  `json:"purchasePriceMinor"`
}

type BillingPlanQuota struct {
	QuotaID       string `json:"quotaId"`
	IncludedUnits string `json:"includedUnits"`
}

type BillingPlan struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	MonthlyPriceMinor int64              `json:"monthlyPriceMinor"`
	Discount3         int                `json:"discount3"`
	Discount6         int                `json:"discount6"`
	Discount12        int                `json:"discount12"`
	IsDefault         bool               `json:"isDefault"`
	Quotas            []BillingPlanQuota `json:"quotas"`
}

// Quotas reads system quotas when integrationID is empty, or one integration's quotas.
// The resource API requires an ordinary user access token.
func (c *BillingClient) Quotas(ctx context.Context, integrationID string) ([]BillingQuota, error) {
	query := url.Values{}
	if integrationID != "" {
		id, err := uuid.Parse(integrationID)
		if err != nil || id == uuid.Nil {
			return nil, ErrInvalidInput
		}
		query.Set("integrationId", id.String())
	}
	var out []BillingQuota
	err := c.client.requestJSON(ctx, "billing.quotas", http.MethodGet, "/billing/quotas", query, nil, &out)
	return out, err
}

// Plans reads visible catalog plans, not the user's assigned plan or balance.
func (c *BillingClient) Plans(ctx context.Context) ([]BillingPlan, error) {
	var out []BillingPlan
	err := c.client.get(ctx, "billing.plans", "/billing/plans", &out)
	return out, err
}
