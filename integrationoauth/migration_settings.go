package integrationoauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

const MigrationSettingsProtocol = "aheron.oauth-migration-settings.v1"

// Settings contain identity and permission generations, never a credential or
// an issuer/resource URL. Endpoints and the private key remain deployment config.
type MigrationSettingsRequest struct {
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

var settingsUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (r MigrationSettingsRequest) Valid() bool {
	for _, id := range []string{r.CommandID, r.IntegrationID, r.JobID, r.ProjectID, r.InstallationID, r.ClientID, r.LegacyKeyID, r.ProofID} {
		if !settingsUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	if r.Protocol != MigrationSettingsProtocol || len(r.KeyID) < 1 || len(r.KeyID) > 128 || !MigrationDigest(r.ProofDigest) || !MigrationDigest(r.PolicyDigest) || r.AccessVersion < 1 || r.IntegrationAccessVersion < 1 || r.GrantVersion < 1 || r.PolicyRevision < 1 {
		return false
	}
	for _, c := range r.KeyID {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func (r MigrationSettingsRequest) Digest() string {
	if !r.Valid() {
		return ""
	}
	b, _ := json.Marshal(r)
	h := sha256.Sum256(append([]byte(MigrationSettingsProtocol+"\n"), b...))
	return hex.EncodeToString(h[:])
}

// Stored acknowledges a durable configuration only. It is NOT evidence that
// runtime/background operations work and MUST NOT authorize an OAuth-only cutover.
type MigrationSettingsReceipt struct {
	Protocol       string `json:"protocol"`
	CommandID      string `json:"commandId"`
	ProjectID      string `json:"projectId"`
	InstallationID string `json:"installationId"`
	RequestDigest  string `json:"requestDigest"`
	State          string `json:"state"`
}

func (r MigrationSettingsRequest) StoredReceipt() MigrationSettingsReceipt {
	return MigrationSettingsReceipt{MigrationSettingsProtocol, r.CommandID, r.ProjectID, r.InstallationID, r.Digest(), "stored"}
}

func (r MigrationSettingsReceipt) Matches(p MigrationSettingsRequest) bool {
	return p.Valid() && r == p.StoredReceipt()
}

func DecodeMigrationSettings(raw []byte) (MigrationSettingsRequest, error) {
	var out MigrationSettingsRequest
	if err := decodeSettingsObject(raw, &out, []string{"protocol", "commandId", "integrationId", "jobId", "projectId", "installationId", "clientId", "keyId", "legacyKeyId", "proofId", "proofDigest", "accessVersion", "integrationAccessVersion", "grantVersion", "policyRevision", "policyDigest"}); err != nil || !out.Valid() {
		return MigrationSettingsRequest{}, errors.New("invalid migration settings")
	}
	return out, nil
}

func DecodeMigrationSettingsReceipt(raw []byte) (MigrationSettingsReceipt, error) {
	var out MigrationSettingsReceipt
	err := decodeSettingsObject(raw, &out, []string{"protocol", "commandId", "projectId", "installationId", "requestDigest", "state"})
	return out, err
}

func decodeSettingsObject(raw []byte, out any, fields []string) error {
	invalid := errors.New("invalid migration settings object")
	if len(raw) == 0 || len(raw) > 8192 || !utf8.Valid(raw) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return invalid
	}
	seen := make(map[string]bool)
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[f] = true
	}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid
		}
		k, ok := t.(string)
		if !ok || !allowed[k] || seen[k] {
			return invalid
		}
		seen[k] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid
		}
	}
	if _, err := d.Token(); err != nil || len(seen) != len(fields) {
		return invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	if json.Unmarshal(raw, out) != nil {
		return invalid
	}
	return nil
}
