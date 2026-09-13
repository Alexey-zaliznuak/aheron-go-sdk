package integrationoauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// MigrationProofRequest is the versioned challenge context shared with auth.
// It is not a grant or a command to create/update an installation.
type MigrationProofRequest struct {
	ClientID             string     `json:"clientId"`
	ProofID              string     `json:"proofId"`
	IntegrationID        string     `json:"integrationId"`
	JobID                string     `json:"jobId"`
	ProjectID            string     `json:"projectId"`
	InstallationID       string     `json:"installationId"`
	LegacyKeyID          string     `json:"legacyKeyId"`
	ClientVersion        int64      `json:"clientVersion"`
	CredentialVersion    int64      `json:"credentialVersion"`
	KeyID                string     `json:"keyId"`
	Nonce                string     `json:"nonce"`
	ExpectedKeyUpdatedAt time.Time  `json:"expectedKeyUpdatedAt"`
	SourceScopes         []string   `json:"sourceScopes"`
	SourceExpiresAt      *time.Time `json:"sourceExpiresAt"`
	CreatedAt            time.Time  `json:"createdAt"`
	ExpiresAt            time.Time  `json:"expiresAt"`
}

func (MigrationProofRequest) String() string   { return "[redacted migration challenge]" }
func (MigrationProofRequest) GoString() string { return "[redacted migration challenge]" }

func (p MigrationProofRequest) Valid() bool {
	for _, id := range []string{p.ClientID, p.ProofID, p.IntegrationID, p.JobID, p.ProjectID, p.InstallationID, p.LegacyKeyID} {
		if !canonicalUUID(id) {
			return false
		}
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(p.Nonce)
	if err != nil || len(nonce) != 32 || len(p.Nonce) != 43 || p.ClientVersion < 1 || p.CredentialVersion < 1 || len(p.KeyID) < 1 || len(p.KeyID) > 128 {
		return false
	}
	for _, c := range p.KeyID {
		if c < 33 || c > 126 {
			return false
		}
	}
	if p.ExpectedKeyUpdatedAt.IsZero() || p.CreatedAt.IsZero() || !p.ExpiresAt.After(p.CreatedAt) || p.ExpiresAt.Sub(p.CreatedAt) > 15*time.Minute || p.SourceScopes == nil || len(p.SourceScopes) > 128 || !slices.IsSorted(p.SourceScopes) {
		return false
	}
	for i, s := range p.SourceScopes {
		if len(s) == 0 || len(s) > 128 || i > 0 && s == p.SourceScopes[i-1] {
			return false
		}
		for _, c := range s {
			if c < 33 || c > 126 || c == '"' || c == '\\' {
				return false
			}
		}
	}
	raw, _ := json.Marshal(p)
	return len(raw) <= 16<<10
}

// Digest mirrors auth's versioned canonical contract: UTC microseconds,
// sorted scopes and SHA-256(nonce) in place of the plaintext challenge.
func (p MigrationProofRequest) Digest() string {
	if !p.Valid() {
		return ""
	}
	nonce, _ := base64.RawURLEncoding.Strict().DecodeString(p.Nonce)
	h := sha256.Sum256(nonce)
	p.Nonce = hex.EncodeToString(h[:])
	p.ExpectedKeyUpdatedAt = p.ExpectedKeyUpdatedAt.UTC().Truncate(time.Microsecond)
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Microsecond)
	p.ExpiresAt = p.ExpiresAt.UTC().Truncate(time.Microsecond)
	if p.SourceExpiresAt != nil {
		t := p.SourceExpiresAt.UTC().Truncate(time.Microsecond)
		p.SourceExpiresAt = &t
	}
	raw, _ := json.Marshal(p)
	d := sha256.Sum256(append([]byte("aheron.oauth-migration-proof.v1\n"), raw...))
	return hex.EncodeToString(d[:])
}

type MigrationProofReceipt struct {
	ClientID             string     `json:"clientId"`
	ProofID              string     `json:"proofId"`
	IntegrationID        string     `json:"integrationId"`
	JobID                string     `json:"jobId"`
	ProjectID            string     `json:"projectId"`
	InstallationID       string     `json:"installationId"`
	LegacyKeyID          string     `json:"legacyKeyId"`
	ClientVersion        int64      `json:"clientVersion"`
	CredentialVersion    int64      `json:"credentialVersion"`
	KeyID                string     `json:"keyId"`
	RequestDigest        string     `json:"requestDigest"`
	State                string     `json:"state"`
	ExpectedKeyUpdatedAt time.Time  `json:"expectedKeyUpdatedAt"`
	SourceScopes         []string   `json:"sourceScopes"`
	SourceExpiresAt      *time.Time `json:"sourceExpiresAt"`
	CreatedAt            time.Time  `json:"createdAt"`
	ExpiresAt            time.Time  `json:"expiresAt"`
}

func (r MigrationProofReceipt) Matches(p MigrationProofRequest) bool {
	digest := p.Digest()
	return digest != "" && r.RequestDigest == digest && r.ClientID == p.ClientID && r.ProofID == p.ProofID && r.IntegrationID == p.IntegrationID && r.JobID == p.JobID && r.ProjectID == p.ProjectID && r.InstallationID == p.InstallationID && r.LegacyKeyID == p.LegacyKeyID && r.ClientVersion == p.ClientVersion && r.CredentialVersion == p.CredentialVersion && r.KeyID == p.KeyID && r.ExpectedKeyUpdatedAt.Equal(p.ExpectedKeyUpdatedAt) && slices.Equal(r.SourceScopes, p.SourceScopes) && sameExpiry(r.SourceExpiresAt, p.SourceExpiresAt) && r.CreatedAt.Equal(p.CreatedAt) && r.ExpiresAt.Equal(p.ExpiresAt) && slices.Contains([]string{"pending", "claimed", "consumed", "cancelled"}, r.State)
}

func sameExpiry(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func MigrationDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
