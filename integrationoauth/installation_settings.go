package integrationoauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

var settingsUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
	return asciiIdentifier(s.KeyID, 128) && s.AccessVersion > 0 && s.IntegrationAccessVersion > 0 && s.GrantVersion > 0 && s.PolicyRevision > 0 && ValidDigest(s.PolicyDigest)
}

// ValidDigest reports whether s is a lowercase hexadecimal SHA-256 digest.
// It is used for OAuth policy integrity and has no migration-specific meaning.
func ValidDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
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

func decodeSettingsObject(raw []byte, out any, fields []string) error {
	if len(raw) == 0 || len(raw) > 8192 || !utf8.Valid(raw) {
		return errors.New("invalid settings object")
	}
	allowed := make(map[string]bool, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid settings object")
	}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || seen[name] {
			return errors.New("invalid settings field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid settings value")
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return errors.New("invalid settings object")
	}
	if token, err = d.Token(); err != io.EOF || len(seen) != len(fields) {
		return errors.New("invalid settings object")
	}
	return json.Unmarshal(raw, out)
}
