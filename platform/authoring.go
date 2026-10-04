package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

// GraphEdit is one edit in an atomic, revisioned draft command. Settings/Data
// are domain JSON, never arbitrary HTTP. Backend owns all graph invariants.
type GraphEdit struct {
	EntityKind string          `json:"entityKind"`
	Action     string          `json:"action"`
	EntityID   string          `json:"entityId,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Cascade    bool            `json:"cascade"`
}
type GraphCommand struct {
	ExpectedNativeContractRevision string      `json:"expectedNativeContractRevision,omitempty"`
	OperationID                    string      `json:"operationId"`
	EditorSessionID                string      `json:"editorSessionId"`
	BaseRevision                   int64       `json:"baseRevision"`
	CommandType                    string      `json:"commandType"`
	Edits                          []GraphEdit `json:"edits"`
	ReversesRevision               *int64      `json:"reversesRevision,omitempty"`
}
type GraphOperation struct {
	SchemeID         string          `json:"schemeId"`
	Revision         int64           `json:"revision"`
	OperationID      string          `json:"operationId"`
	ActorUserID      string          `json:"actorUserId"`
	EditorSessionID  string          `json:"editorSessionId"`
	CommandType      string          `json:"commandType"`
	PayloadVersion   int             `json:"payloadVersion"`
	Payload          json.RawMessage `json:"payload"`
	BaseRevision     int64           `json:"baseRevision"`
	ReversesRevision *int64          `json:"reversesRevision,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
}
type NativeBlockDescription struct {
	ProviderKey        string          `json:"providerKey,omitempty"`
	ContractRevision   string          `json:"contractRevision,omitempty"`
	DocumentationTopic string          `json:"documentationTopic,omitempty"`
	Type               string          `json:"type"`
	Description        string          `json:"description"`
	SettingsSchema     json.RawMessage `json:"settingsSchema"`
}
type PreparedNativeBlock struct {
	ContractRevision string          `json:"contractRevision,omitempty"`
	Type             string          `json:"type"`
	Settings         json.RawMessage `json:"settings"`
	InputKeys        []string        `json:"inputKeys"`
	OutputKeys       []string        `json:"outputKeys"`
}

func (s *SchemesClient) ListOperations(ctx context.Context, projectID, schemeID string, afterRevision int64) ([]GraphOperation, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) || afterRevision < 0 {
		return nil, ErrInvalidInput
	}
	var out []GraphOperation
	err := s.client.requestJSON(ctx, "list graph operations", http.MethodGet, "/projects/"+projectID+"/schemes/"+schemeID+"/operations", url.Values{"afterRevision": {strconv.FormatInt(afterRevision, 10)}}, nil, &out)
	if err != nil {
		return nil, err
	}
	for _, op := range out {
		if !strings.EqualFold(op.SchemeID, schemeID) || op.Revision <= afterRevision || !resourceID.MatchString(op.OperationID) {
			return nil, ErrResponse
		}
	}
	return out, nil
}

// ApplyGraphCommand never retries. To resolve an uncertain outcome, reuse the
// exact operationId and command; the backend returns its committed receipt.
// A 409 requires reading the graph and reconciling intent, not blindly rebasing.
func (s *SchemesClient) ApplyGraphCommand(ctx context.Context, projectID, schemeID string, command GraphCommand) (GraphOperation, error) {
	if (command.ExpectedNativeContractRevision != "" && !docs.ValidKey(command.ExpectedNativeContractRevision)) || !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) || !resourceID.MatchString(command.OperationID) || !resourceID.MatchString(command.EditorSessionID) || command.BaseRevision < 0 || len(command.Edits) == 0 || len(command.Edits) > 1000 || command.CommandType == "" || len(command.CommandType) > 80 {
		return GraphOperation{}, ErrInvalidInput
	}
	var out GraphOperation
	err := s.client.requestJSON(ctx, "apply graph command", http.MethodPost, "/projects/"+projectID+"/schemes/"+schemeID+"/operations", nil, command, &out)
	if err == nil && (!strings.EqualFold(out.SchemeID, schemeID) || !strings.EqualFold(out.OperationID, command.OperationID) || out.Revision < 1) {
		err = ErrResponse
	}
	return out, err
}

func (s *SchemesClient) NativeBlocks(ctx context.Context, projectID string) ([]NativeBlockDescription, error) {
	if !resourceID.MatchString(projectID) {
		return nil, ErrInvalidInput
	}
	var out []NativeBlockDescription
	err := s.client.requestJSON(ctx, "describe native blocks", http.MethodGet, "/projects/"+projectID+"/native-blocks", nil, nil, &out)
	if err != nil {
		return nil, err
	}
	revision := ""
	for i, b := range out {
		if i == 0 {
			revision = b.ContractRevision
		}
		if b.ContractRevision != revision {
			return nil, ErrResponse
		}
		if b.ContractRevision != "" || b.ProviderKey != "" || b.DocumentationTopic != "" {
			if b.ProviderKey != "platform/execution" || !docs.ValidKey(b.ContractRevision) || !docs.ValidKey(b.DocumentationTopic) {
				return nil, ErrResponse
			}
		}
	}
	return out, nil
}
func (s *SchemesClient) PrepareNativeBlock(ctx context.Context, projectID, stepType string, settings json.RawMessage) (PreparedNativeBlock, error) {
	return s.prepareNativeBlock(ctx, projectID, stepType, settings, "")
}

// PrepareNativeBlockAtContract fails closed when the responding runtime cannot
// confirm the caller's observed contract. Never retries against a new revision.
func (s *SchemesClient) PrepareNativeBlockAtContract(ctx context.Context, projectID, stepType string, settings json.RawMessage, expected string) (PreparedNativeBlock, error) {
	if !docs.ValidKey(expected) {
		return PreparedNativeBlock{}, ErrInvalidInput
	}
	return s.prepareNativeBlock(ctx, projectID, stepType, settings, expected)
}

func (s *SchemesClient) prepareNativeBlock(ctx context.Context, projectID, stepType string, settings json.RawMessage, expected string) (PreparedNativeBlock, error) {
	if !resourceID.MatchString(projectID) || stepType == "" || len(stepType) > 80 || len(settings) > 64<<10 || !json.Valid(settings) {
		return PreparedNativeBlock{}, ErrInvalidInput
	}
	var out PreparedNativeBlock
	err := s.client.requestJSON(ctx, "prepare native block", http.MethodPost, "/projects/"+projectID+"/native-blocks/prepare", nil, struct {
		Type                     string          `json:"type"`
		Settings                 json.RawMessage `json:"settings"`
		ExpectedContractRevision string          `json:"expectedContractRevision,omitempty"`
	}{stepType, settings, expected}, &out)
	if err == nil && (out.Type != stepType || !json.Valid(out.Settings) || (expected != "" && out.ContractRevision != expected)) {
		err = ErrResponse
	}
	return out, err
}

// requestJSON sends once with the same credential/redirect/size boundaries as
// reads. It also supports the fixed typed resource paths used for writes.
func (c *Client) requestJSON(ctx context.Context, operation, method, path string, query url.Values, input, out any) error {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil || len(body) > 2<<20 {
			return ErrInvalidInput
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return ErrConfig
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		problem := &APIError{Operation: operation, StatusCode: resp.StatusCode}
		if resp.StatusCode == http.StatusConflict {
			var body struct {
				Code string `json:"code"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil && body.Code == "contract_revision_mismatch" {
				problem.Code = body.Code
			}
		}
		return problem
	}
	if out == nil {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrTransport
	}
	if int64(len(data)) > c.maxResponseBytes {
		return ErrResponseTooLarge
	}
	if strings.TrimSpace(string(data)) == "null" || json.Unmarshal(data, out) != nil {
		return ErrResponse
	}
	return nil
}
