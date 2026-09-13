package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
)

// CRMOAuthConfig binds all CRM methods to one installation. Reuse Provider
// across installations; its token cache stays scoped to project/install/scopes.
type CRMOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

type crmOAuth struct {
	projectID, baseURL                     string
	read, write, variables, writeVariables *integrationoauth.Client
}

func newCRMOAuth(baseURL string, cfg *CRMOAuthConfig) (*crmOAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	c := &crmOAuth{projectID: cfg.ProjectID, baseURL: strings.TrimRight(baseURL, "/")}
	for _, operation := range []struct {
		target **integrationoauth.Client
		scopes []string
	}{
		{&c.read, []string{"crm.read"}}, {&c.write, []string{"crm.write"}},
		{&c.variables, []string{"variables.write"}}, {&c.writeVariables, []string{"crm.write", "variables.write"}},
	} {
		client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "crm", Scopes: operation.scopes}})
		if err != nil {
			return nil, err
		}
		*operation.target = client
	}
	return c, nil
}

var errCRMOAuthResponse = errors.New("integration: invalid CRM OAuth response")

// operation is an explicit inventory of typed CRM methods, not a generic
// write-by-HTTP-method guess. New methods remain unavailable until classified.
func (c *crmOAuth) operation(req httpclient.Request) (*integrationoauth.Client, []int, error) {
	parts := strings.Split(strings.TrimPrefix(req.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "projects" || parts[1] != c.projectID {
		return nil, nil, integrationoauth.ErrRequest
	}
	tail := parts[2:]
	method := req.Method
	if len(tail) == 2 && tail[0] == "subjects" && tail[1] == "upsert" && method == http.MethodPost {
		var body upsertSubjectBody
		if json.Unmarshal(req.Body, &body) != nil {
			return nil, nil, integrationoauth.ErrRequest
		}
		client := c.write
		for _, items := range [][]fieldWire{body.Create, body.Update} {
			for _, item := range items {
				if item.Field != "displayName" && item.Field != "display_name" && item.Field != "description" {
					client = c.writeVariables
				}
			}
		}
		return client, []int{200, 201}, nil
	}
	shape := strings.Join(tail, "/")
	for _, index := range []int{1} {
		if len(tail) > index && (tail[0] == "subjects" || tail[0] == "integrations" || (len(tail) == 2 && (tail[0] == "tags" || tail[0] == "variable-definitions"))) {
			if !integrationOAuthUUID(tail[index]) {
				return nil, nil, integrationoauth.ErrRequest
			}
			copyParts := append([]string(nil), tail...)
			copyParts[index] = "{id}"
			shape = strings.Join(copyParts, "/")
		}
	}
	switch method + " " + shape {
	case "GET subjects/{id}", "GET subjects/{id}/variable-values", "GET variable-definitions", "GET variable-definitions/{id}", "GET tags":
		return c.read, []int{200}, nil
	case "PUT subjects/{id}/variable-values", "PATCH variable-definitions/{id}":
		return c.variables, []int{200}, nil
	case "POST variable-definitions", "POST integrations/{id}/variable-definitions":
		return c.variables, []int{201}, nil
	case "DELETE variable-definitions/{id}":
		return c.variables, []int{204}, nil
	case "POST tags":
		return c.write, []int{201}, nil
	case "PATCH tags/{id}":
		return c.write, []int{200}, nil
	case "DELETE tags/{id}":
		return c.write, []int{204}, nil
	default:
		return nil, nil, integrationoauth.ErrRequest
	}
}

func (c *crmOAuth) do(ctx context.Context, input httpclient.Request) (*httpclient.Response, error) {
	client, statuses, err := c.operation(input)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(c.baseURL + input.Path)
	if err != nil {
		return nil, integrationoauth.ErrRequest
	}
	query := u.Query()
	for k, v := range input.Query {
		query.Set(k, v)
	}
	u.RawQuery = query.Encode()
	var req *http.Request
	if input.Body == nil {
		req, err = http.NewRequestWithContext(ctx, input.Method, u.String(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, input.Method, u.String(), bytes.NewReader(input.Body))
	}
	if err != nil {
		return nil, integrationoauth.ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	if input.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// No automatic write retry: CRM writes have no durable idempotency receipts.
	response, err := client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIError{Method: input.Method, URL: "/projects/{projectId}", Status: response.StatusCode, Message: "CRM OAuth request rejected"}
	}
	accepted := false
	for _, status := range statuses {
		if response.StatusCode == status {
			accepted = true
		}
	}
	if !accepted {
		return nil, errCRMOAuthResponse
	}
	if response.StatusCode == 204 {
		return &httpclient.Response{Status: 204}, nil
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, errCRMOAuthResponse
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errCRMOAuthResponse
	}
	return &httpclient.Response{Status: response.StatusCode, Body: raw}, nil
}
