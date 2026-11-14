package platform

import (
	"context"
	"github.com/google/uuid"
	"net/http"
	"time"
)

type PaymentAccount struct {
	ID           string    `json:"id"`
	OwnerUserID  string    `json:"ownerUserId"`
	Name         string    `json:"name"`
	BalanceMinor int64     `json:"balanceMinor"`
	Paused       bool      `json:"paused"`
	Revision     int64     `json:"revision"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
type ProjectSubscription struct {
	ProjectID        string    `json:"projectId"`
	PaymentAccountID string    `json:"paymentAccountId"`
	PlanID           string    `json:"planId"`
	PlanRevision     int64     `json:"planRevision"`
	PeriodID         string    `json:"periodId"`
	PeriodStart      time.Time `json:"periodStart"`
	PeriodEnd        time.Time `json:"periodEnd"`
	PeriodPriceMinor int64     `json:"periodPriceMinor"`
	Revision         int64     `json:"revision"`
}
type ProjectQuotaBalance struct {
	ProjectID string  `json:"projectId"`
	QuotaID   string  `json:"quotaId"`
	Source    string  `json:"source"`
	Remaining string  `json:"remaining"`
	Granted   string  `json:"granted"`
	PeriodID  *string `json:"periodId"`
	Revision  int64   `json:"revision"`
}
type ProjectQuotaDefinition struct {
	ID                 string `json:"id"`
	OwnerKey           string `json:"ownerKey"`
	SystemName         string `json:"systemName"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	UnitName           string `json:"unitName"`
	BillingStep        int64  `json:"billingStep"`
	PurchasePriceMinor *int64 `json:"purchasePriceMinor"`
}
type ProjectBilling struct {
	Subscription         ProjectSubscription      `json:"subscription"`
	Plan                 BillingPlan              `json:"plan"`
	Quotas               []ProjectQuotaBalance    `json:"quotas"`
	QuotaDefinitions     []ProjectQuotaDefinition `json:"quotaDefinitions"`
	PaymentAccountName   string                   `json:"paymentAccountName"`
	PaymentAccountPaused bool                     `json:"paymentAccountPaused"`
	CanManage            bool                     `json:"canManage"`
}
type UpdatePaymentAccount struct {
	Name        string `json:"name"`
	Paused      *bool  `json:"paused,omitempty"`
	Revision    int64  `json:"revision"`
	OperationID string `json:"operationId"`
}
type ChangeProjectPlan struct {
	ProjectID   string `json:"projectId"`
	PlanID      string `json:"planId"`
	Revision    int64  `json:"revision"`
	OperationID string `json:"operationId"`
}

func validBillingIDs(ids ...string) bool {
	for _, id := range ids {
		u, e := uuid.Parse(id)
		if e != nil || u == uuid.Nil {
			return false
		}
	}
	return true
}

// Accounts may initialize the caller's primary zero-balance account.
func (c *BillingClient) Accounts(ctx context.Context) ([]PaymentAccount, error) {
	var out []PaymentAccount
	err := c.client.get(ctx, "billing.accounts", "/billing/accounts", &out)
	return out, err
}
func (c *BillingClient) CreateAccount(ctx context.Context, id, name string) (PaymentAccount, error) {
	var out PaymentAccount
	u, err := uuid.Parse(id)
	if err != nil || u.Version() != 4 {
		return out, ErrInvalidInput
	}
	err = c.client.requestJSON(ctx, "billing.accounts.create", http.MethodPost, "/billing/accounts", nil, struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{id, name}, &out)
	return out, err
}
func (c *BillingClient) UpdateAccount(ctx context.Context, id string, in UpdatePaymentAccount) (PaymentAccount, error) {
	var out PaymentAccount
	if !validBillingIDs(id, in.OperationID) || in.Revision < 1 {
		return out, ErrInvalidInput
	}
	err := c.client.requestJSON(ctx, "billing.accounts.update", http.MethodPatch, "/billing/accounts/"+id, nil, in, &out)
	return out, err
}

// Project may initialize the free default subscription after checking project membership.
func (c *BillingClient) Project(ctx context.Context, id string) (ProjectBilling, error) {
	var out ProjectBilling
	if !validBillingIDs(id) {
		return out, ErrInvalidInput
	}
	err := c.client.get(ctx, "billing.project", "/billing/projects/"+id, &out)
	return out, err
}

// ChangePlan charges immediately; retry only with identical operationId/body.
func (c *BillingClient) ChangePlan(ctx context.Context, in ChangeProjectPlan) (ProjectSubscription, error) {
	var out ProjectSubscription
	if !validBillingIDs(in.ProjectID, in.PlanID, in.OperationID) || in.Revision < 1 {
		return out, ErrInvalidInput
	}
	err := c.client.requestJSON(ctx, "billing.project.plan", http.MethodPut, "/billing/projects/"+in.ProjectID+"/plan", nil, in, &out)
	return out, err
}
func (c *BillingClient) AttachAccount(ctx context.Context, projectID, accountID string, revision int64) (ProjectSubscription, error) {
	var out ProjectSubscription
	if !validBillingIDs(projectID, accountID) || revision < 1 {
		return out, ErrInvalidInput
	}
	in := struct {
		PaymentAccountID string `json:"paymentAccountId"`
		Revision         int64  `json:"revision"`
	}{accountID, revision}
	err := c.client.requestJSON(ctx, "billing.project.account", http.MethodPut, "/billing/projects/"+projectID+"/account", nil, in, &out)
	return out, err
}
