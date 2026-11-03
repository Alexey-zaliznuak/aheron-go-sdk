package integration

import (
	"context"
	"errors"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
	"net/http"
	"strconv"
)

func (c *AnalyticsClient) experimentPath(project, experiment string, ids ...string) (string, error) {
	if c == nil || c.definitionsRead == nil || project != c.projectID || !integrationOAuthUUID(project) || !integrationOAuthUUID(experiment) {
		return "", integrationoauth.ErrRequest
	}
	for _, id := range ids {
		if !integrationOAuthUUID(id) {
			return "", integrationoauth.ErrRequest
		}
	}
	return "/clients/projects/" + project + "/experiments/" + experiment, nil
}

func (c *AnalyticsClient) CreateStageSet(ctx context.Context, project, experiment, id string, in platform.AnalyticsSaveStageSet) (out platform.AnalyticsStageSet, err error) {
	p, err := c.experimentPath(project, experiment, id, in.OperationID)
	if err != nil {
		return out, err
	}
	if in.ExpectedRevision != "0" {
		return out, integrationoauth.ErrRequest
	}
	err = callResourceJSON(ctx, c.definitionsWrite, c.baseURL, http.MethodPut, p+"/stage-sets/"+id, in, &out, true, http.StatusOK)
	if err == nil && (out.ID != id || out.Revision == "" || out.Stages == nil) {
		err = errors.New("invalid stage set response")
	}
	return
}

func (c *AnalyticsClient) StageGraph(ctx context.Context, project, experiment, id string) (out platform.AnalyticsStageGraph, err error) {
	p, err := c.experimentPath(project, experiment, id)
	if err != nil {
		return out, err
	}
	err = callResourceJSON(ctx, c.definitionsRead, c.baseURL, http.MethodGet, p+"/stage-sets/"+id+"/graph", nil, &out, true, http.StatusOK)
	if err == nil && (out.StageSetID != id || out.Nodes == nil) {
		err = errors.New("invalid stage graph response")
	}
	return
}

func (c *AnalyticsClient) AudienceSize(ctx context.Context, project, experiment, id string) (out platform.AnalyticsAudienceSize, err error) {
	p, err := c.experimentPath(project, experiment, id)
	if err != nil {
		return out, err
	}
	err = callResourceJSON(ctx, c.definitionsRead, c.baseURL, http.MethodGet, p+"/audiences/"+id+"/size", nil, &out, true, http.StatusOK)
	if err == nil {
		if _, e := strconv.ParseUint(out.Value, 10, 64); e != nil {
			err = errors.New("invalid audience size response")
		}
	}
	return
}

func (c *AnalyticsClient) Apply(ctx context.Context, project, experiment string, in platform.AnalyticsCommand) (out platform.AnalyticsReceipt, err error) {
	p, err := c.experimentPath(project, experiment, in.SubjectID, in.OperationID)
	if err != nil {
		return out, err
	}
	err = callResourceJSON(ctx, c.definitionsWrite, c.baseURL, http.MethodPost, p+"/commands", in, &out, true, http.StatusOK)
	if err == nil && (out.OperationID != in.OperationID || out.AcceptedAt.IsZero()) {
		err = errors.New("invalid analytics command receipt")
	}
	return
}
