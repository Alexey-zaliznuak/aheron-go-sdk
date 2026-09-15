package integrationoauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const retainedInstallationSettingsProtocol = "aheron.oauth-migration-settings.v1"

// DecodeRetainedInstallationIdentity decodes the OAuth identity retained in
// the historical installation state column during the completed cutover. It
// only reads and validates the identity; it cannot authorize a command or
// create migration state. The returned digest covers every stored setting and
// is suitable for cache revision fencing.
func DecodeRetainedInstallationIdentity(raw []byte) (*InstallationSettings, string, error) {
	if len(raw) == 0 {
		return nil, "", nil
	}
	var state struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := decodeRetainedObject(raw, &state,
		[]string{"pending", "legacyKeyDigest", "settings"}, nil,
		map[string]bool{"pending": true, "legacyKeyDigest": true, "settings": true}); err != nil {
		return nil, "", ErrRequest
	}
	if len(state.Settings) == 0 || bytes.Equal(bytes.TrimSpace(state.Settings), []byte("null")) {
		return nil, "", nil
	}

	var stored struct {
		Protocol                 string `json:"protocol"`
		CommandID                string `json:"commandId"`
		IntegrationID            string `json:"integrationId"`
		JobID                    string `json:"jobId"`
		ProjectID                string `json:"projectId"`
		InstallationID           string `json:"installationId"`
		ClientID                 string `json:"clientId"`
		KeyID                    string `json:"keyId"`
		LegacyKeyID              string `json:"legacyKeyId"`
		ProofID                  string `json:"proofId"`
		ProofDigest              string `json:"proofDigest"`
		AccessVersion            int64  `json:"accessVersion"`
		IntegrationAccessVersion int64  `json:"integrationAccessVersion"`
		GrantVersion             int64  `json:"grantVersion"`
		PolicyRevision           int64  `json:"policyRevision"`
		PolicyDigest             string `json:"policyDigest"`
	}
	fields := []string{"protocol", "commandId", "integrationId", "jobId", "projectId", "installationId", "clientId", "keyId", "legacyKeyId", "proofId", "proofDigest", "accessVersion", "integrationAccessVersion", "grantVersion", "policyRevision", "policyDigest"}
	if err := decodeRetainedObject(state.Settings, &stored, fields, fields, nil); err != nil ||
		stored.Protocol != retainedInstallationSettingsProtocol ||
		!canonicalUUID(stored.CommandID) || !canonicalUUID(stored.IntegrationID) || !canonicalUUID(stored.JobID) ||
		!canonicalUUID(stored.ProjectID) || !canonicalUUID(stored.InstallationID) || !canonicalUUID(stored.ClientID) ||
		!canonicalUUID(stored.LegacyKeyID) || !canonicalUUID(stored.ProofID) ||
		!retainedKeyID(stored.KeyID) || !ValidDigest(stored.ProofDigest) || !ValidDigest(stored.PolicyDigest) ||
		stored.AccessVersion < 1 || stored.IntegrationAccessVersion < 1 || stored.GrantVersion < 1 || stored.PolicyRevision < 1 {
		return nil, "", ErrRequest
	}
	settings := &InstallationSettings{
		IntegrationID: stored.IntegrationID, ProjectID: stored.ProjectID, InstallationID: stored.InstallationID,
		ClientID: stored.ClientID, KeyID: stored.KeyID, AccessVersion: stored.AccessVersion,
		IntegrationAccessVersion: stored.IntegrationAccessVersion, GrantVersion: stored.GrantVersion,
		PolicyRevision: stored.PolicyRevision, PolicyDigest: stored.PolicyDigest,
	}
	if !settings.Valid() {
		return nil, "", ErrRequest
	}
	canonical, _ := json.Marshal(stored)
	digest := sha256.Sum256(append([]byte(retainedInstallationSettingsProtocol+"\n"), canonical...))
	return settings, hex.EncodeToString(digest[:]), nil
}

func retainedKeyID(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func decodeRetainedObject(raw []byte, dst any, fields, required []string, nullable map[string]bool) error {
	if len(raw) == 0 || len(raw) > 16<<10 {
		return errors.New("invalid retained object")
	}
	allowed := make(map[string]bool, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("object required")
	}
	for decoder.More() {
		token, err = decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || !allowed[field] || seen[field] {
			return errors.New("invalid field")
		}
		seen[field] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || (nullable == nil || !nullable[field]) && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid value")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("unterminated object")
	}
	if token, err = decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	for _, field := range required {
		if !seen[field] {
			return errors.New("missing field")
		}
	}
	return json.Unmarshal(raw, dst)
}
