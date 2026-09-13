package integrationoauth

import (
	"encoding/json"
)

// InstallationSettings is the public OAuth configuration of a newly installed
// lifetime. It contains no migration evidence, credentials or deployment URLs.
// Persist it atomically with the signed lifecycle watermark; uninstall clears it.
type InstallationSettings struct {
	IntegrationID            string `json:"integrationId"`
	ProjectID                string `json:"projectId"`
	InstallationID           string `json:"installationId"`
	ClientID                 string `json:"clientId"`
	KeyID                    string `json:"keyId"`
	AccessVersion            int64  `json:"accessVersion"`
	IntegrationAccessVersion int64  `json:"integrationAccessVersion"`
	GrantVersion             int64  `json:"grantVersion"`
	PolicyRevision           int64  `json:"policyRevision"`
	PolicyDigest             string `json:"policyDigest"`
}

func (s InstallationSettings) Valid() bool {
	for _, id := range []string{s.IntegrationID, s.ProjectID, s.InstallationID, s.ClientID} {
		if !settingsUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	return asciiIdentifier(s.KeyID, 128) && s.AccessVersion > 0 && s.IntegrationAccessVersion > 0 && s.GrantVersion > 0 && s.PolicyRevision > 0 && MigrationDigest(s.PolicyDigest)
}

// Decode strictly even inside a lifecycle request: encoding/json alone accepts
// duplicate, mis-cased and unknown fields and can erase security bindings.
func (s *InstallationSettings) UnmarshalJSON(raw []byte) error {
	type plain InstallationSettings
	var value plain
	if err := decodeSettingsObject(raw, &value, []string{"integrationId", "projectId", "installationId", "clientId", "keyId", "accessVersion", "integrationAccessVersion", "grantVersion", "policyRevision", "policyDigest"}); err != nil {
		return ErrRequest
	}
	if !InstallationSettings(value).Valid() {
		return ErrRequest
	}
	*s = InstallationSettings(value)
	return nil
}

func DecodeInstallationSettings(raw []byte) (InstallationSettings, error) {
	var s InstallationSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return InstallationSettings{}, ErrRequest
	}
	return s, nil
}
