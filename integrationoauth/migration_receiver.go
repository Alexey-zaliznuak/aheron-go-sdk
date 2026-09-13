package integrationoauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
)

// MigrationInstallation is an existing local installation snapshot. Generation
// is an immutable local lifetime ID, changed on every reinstall. A repository
// must return ErrRequest for missing/deleted rows; the receiver never creates one.
type MigrationInstallation struct {
	IntegrationID  string
	ProjectID      string
	Generation     string
	InstallationID string
	Active         bool
	LegacyAPIKey   string
	Migration      MigrationReceiverState
}

func (MigrationInstallation) String() string   { return "[redacted migration installation]" }
func (MigrationInstallation) GoString() string { return "[redacted migration installation]" }

// Pending is a locally saved challenge context, NOT proof of possession. It is
// saved before auth HTTP so a lost reply cannot strand later settings delivery.
// The raw credential and nonce are deliberately excluded from this record.
type MigrationReceiverState struct {
	Pending         *MigrationProofReceipt    `json:"pending,omitempty"`
	LegacyKeyDigest string                    `json:"legacyKeyDigest,omitempty"`
	Settings        *MigrationSettingsRequest `json:"settings,omitempty"`
}

// Store owns the installation transaction. CAS MUST reread and compare the
// complete expected snapshot (including generation, credential, active status
// and migration state), then update only migration state/installationId. Never
// upsert missing installations, activate them, or replace their credential.
// Uninstall clears credentials/settings and retains its normal tombstone.
type MigrationReceiverStore interface {
	ReadMigrationInstallation(context.Context, string) (MigrationInstallation, error)
	CompareAndSwapMigrationInstallation(context.Context, MigrationInstallation, MigrationInstallation) error
}

type MigrationReceiver struct {
	proof *MigrationProofClient
	store MigrationReceiverStore
}

func NewMigrationReceiver(proof *MigrationProofClient, store MigrationReceiverStore) (*MigrationReceiver, error) {
	if proof == nil || store == nil {
		return nil, ErrConfig
	}
	return &MigrationReceiver{proof: proof, store: store}, nil
}

func (r *MigrationReceiver) read(ctx context.Context, project, installation string) (MigrationInstallation, error) {
	s, err := r.store.ReadMigrationInstallation(ctx, project)
	if err != nil {
		return s, err
	}
	if s.IntegrationID != r.proof.integrationID || s.ProjectID != project || s.Generation == "" || len(s.Generation) > 128 || !s.Active || !asciiIdentifier(s.LegacyAPIKey, 512) || s.InstallationID != "" && s.InstallationID != installation {
		return MigrationInstallation{}, ErrRequest
	}
	if c := s.Migration.Settings; c != nil && (!c.Valid() || c.InstallationID != installation || c.ProjectID != project || c.IntegrationID != s.IntegrationID) {
		return MigrationInstallation{}, ErrRequest
	}
	return s, nil
}

func migrationKeyDigest(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func cloneMigrationInstallation(in MigrationInstallation) MigrationInstallation {
	// Copy only typed public state; credentials are never serialized.
	raw, _ := json.Marshal(in.Migration)
	var state MigrationReceiverState
	_ = json.Unmarshal(raw, &state)
	in.Migration = state
	return in
}

func (r *MigrationReceiver) SubmitProof(ctx context.Context, c MigrationChallenge) (MigrationProofReceipt, error) {
	if r == nil || !r.proof.Accepts(c) {
		return MigrationProofReceipt{}, ErrRequest
	}
	before, err := r.read(ctx, c.ProjectID, c.InstallationID)
	if err != nil {
		return MigrationProofReceipt{}, err
	}
	if old := before.Migration.Settings; old != nil && (old.ClientID != c.ClientID || old.KeyID != c.KeyID) {
		return MigrationProofReceipt{}, ErrRequest
	}
	if p := before.Migration.Pending; p != nil {
		if p.ProofID == c.ProofID && !p.Matches(c.MigrationProofRequest) || p.CreatedAt.After(c.CreatedAt) {
			return MigrationProofReceipt{}, ErrRequest
		}
	}
	after := cloneMigrationInstallation(before)
	// Typed receipt-shaped context is marked pending, never claimed/consumed.
	raw, _ := json.Marshal(c.MigrationProofRequest)
	var pending MigrationProofReceipt
	_ = json.Unmarshal(raw, &pending)
	pending.RequestDigest, pending.State = c.RequestDigest, "pending"
	after.Migration.Pending = &pending
	after.Migration.LegacyKeyDigest = migrationKeyDigest(before.LegacyAPIKey)
	// Even an exact retry performs CAS: deletion/reinstall can race this read.
	if err := r.store.CompareAndSwapMigrationInstallation(ctx, before, after); err != nil {
		return MigrationProofReceipt{}, err
	}
	return r.proof.Submit(ctx, c, before.LegacyAPIKey)
}

// StoreSettings must only be called after verification of the platform signature
// in MigrationSettingsProtocol. It saves configuration, never asserts readiness.
func (r *MigrationReceiver) StoreSettings(ctx context.Context, c MigrationSettingsRequest) (MigrationSettingsReceipt, error) {
	if r == nil || !c.Valid() || c.IntegrationID != r.proof.integrationID || c.KeyID != r.proof.keyID {
		return MigrationSettingsReceipt{}, ErrRequest
	}
	before, err := r.read(ctx, c.ProjectID, c.InstallationID)
	if err != nil {
		return MigrationSettingsReceipt{}, err
	}
	p := before.Migration.Pending
	if p == nil {
		return MigrationSettingsReceipt{}, ErrMigrationProof
	} // pending durable proof may still be committing
	if p.State != "pending" || p.IntegrationID != c.IntegrationID || p.ProjectID != c.ProjectID || p.InstallationID != c.InstallationID || p.ClientID != c.ClientID || p.KeyID != c.KeyID || p.JobID != c.JobID || p.ProofID != c.ProofID || p.RequestDigest != c.ProofDigest || p.LegacyKeyID != c.LegacyKeyID || before.Migration.LegacyKeyDigest != migrationKeyDigest(before.LegacyAPIKey) {
		return MigrationSettingsReceipt{}, ErrRequest
	}
	if old := before.Migration.Settings; old != nil {
		if old.ClientID != c.ClientID || old.KeyID != c.KeyID || old.AccessVersion > c.AccessVersion || old.IntegrationAccessVersion > c.IntegrationAccessVersion || old.GrantVersion > c.GrantVersion || old.CommandID == c.CommandID && !reflect.DeepEqual(*old, c) || old.AccessVersion == c.AccessVersion && !reflect.DeepEqual(*old, c) {
			return MigrationSettingsReceipt{}, ErrRequest
		}
	}
	after := cloneMigrationInstallation(before)
	after.InstallationID = c.InstallationID
	after.Migration.Settings = &c
	if err := r.store.CompareAndSwapMigrationInstallation(ctx, before, after); err != nil {
		return MigrationSettingsReceipt{}, err
	}
	return c.StoredReceipt(), nil
}
