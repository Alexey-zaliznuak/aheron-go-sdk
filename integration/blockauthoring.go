package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var ErrBlockAuthoringInvalid = errors.New("integration: invalid block authoring request")

// BlockAuthoringRequest prepares settings without saving or executing a block.
// Editing supplies authoritative BaseSettings and an RFC 7396 merge patch.
// Objects merge recursively; null removes a field; arrays replace as a whole.
type BlockAuthoringRequest struct {
	SchemeID           string          `json:"schemeId"`
	BlockKey           string          `json:"blockKey"`
	IntegrationVersion int             `json:"integrationVersion"`
	Settings           json.RawMessage `json:"settings,omitempty"`
	BaseSettings       json.RawMessage `json:"baseSettings,omitempty"`
	Changes            json.RawMessage `json:"changes,omitempty"`
}

type PreparedIntegrationBlock struct {
	BlockKey           string          `json:"blockKey"`
	IntegrationVersion int             `json:"integrationVersion"`
	Settings           json.RawMessage `json:"settings"`
	InputKeys          []string        `json:"inputKeys"`
	OutputKeys         []string        `json:"outputKeys"`
	RemovedOutputKeys  []string        `json:"removedOutputKeys"`
}

// AuthoringSettings preserves unknown, unedited fields. Providers must reject
// changes to fields they cannot validate, rather than silently stripping them.
func AuthoringSettings(req BlockAuthoringRequest) (json.RawMessage, error) {
	decode := func(raw json.RawMessage) (map[string]any, error) {
		if len(raw) == 0 || len(raw) > 64<<10 {
			return nil, ErrBlockAuthoringInvalid
		}
		var value map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&value) != nil || value == nil || d.Decode(new(any)) != io.EOF {
			return nil, ErrBlockAuthoringInvalid
		}
		return value, nil
	}
	if len(req.BaseSettings) == 0 && len(req.Changes) == 0 {
		if _, err := decode(req.Settings); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), req.Settings...), nil
	}
	if len(req.Settings) != 0 {
		return nil, ErrBlockAuthoringInvalid
	}
	base, err := decode(req.BaseSettings)
	if err != nil {
		return nil, err
	}
	patch, err := decode(req.Changes)
	if err != nil {
		return nil, err
	}
	mergeAuthoringObject(base, patch)
	result, err := json.Marshal(base)
	if err != nil || len(result) > 64<<10 {
		return nil, ErrBlockAuthoringInvalid
	}
	return result, nil
}

func mergeAuthoringObject(base, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(base, key)
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			base[key] = value
			continue
		}
		target, ok := base[key].(map[string]any)
		if !ok {
			target = map[string]any{}
		}
		mergeAuthoringObject(target, object)
		base[key] = target
	}
}
