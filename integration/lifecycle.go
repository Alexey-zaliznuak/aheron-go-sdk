package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	LifecycleProtocol   = "aheron.installation-lifecycle.v1"
	LifecycleInstall    = "install"
	LifecycleUninstall  = "uninstall"
	LifecycleApplied    = "applied"
	LifecycleDuplicate  = "duplicate"
	LifecycleSuperseded = "superseded"
)

var (
	ErrLifecycleInvalid  = errors.New("integration: invalid lifecycle message")
	ErrLifecycleConflict = errors.New("integration: conflicting lifecycle message")
	ErrLifecycleState    = errors.New("integration: invalid persisted lifecycle state")
	lifecycleUUID        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// LifecycleRequest is a signed message for a dedicated lifecycle endpoint.
// Sequence is allocated by backend for (projectId, integrationId) and NEVER
// resets after uninstall/reinstall. Installation accessVersion is not a substitute.
// EventID and the whole message remain immutable across delivery attempts.
// ProjectAPIKey may accompany install during the API-key bridge. An install
// without it must clear any previous key; OAuth credentials stay out of this body.
type LifecycleRequest struct {
	Protocol       string `json:"protocol"`
	EventID        string `json:"eventId"`
	IntegrationID  string `json:"integrationId"`
	ProjectID      string `json:"projectId"`
	InstallationID string `json:"installationId"`
	Sequence       int64  `json:"sequence"`
	Action         string `json:"action"`
	ProjectAPIKey  string `json:"projectApiKey,omitempty"`
}

func lifecycleValidID(id string) bool {
	return lifecycleUUID.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func (r LifecycleRequest) Validate() error {
	if r.Protocol != LifecycleProtocol || !lifecycleValidID(r.EventID) || !lifecycleValidID(r.IntegrationID) || !lifecycleValidID(r.ProjectID) || !lifecycleValidID(r.InstallationID) || r.Sequence < 1 {
		return ErrLifecycleInvalid
	}
	if r.Action != LifecycleInstall && r.Action != LifecycleUninstall {
		return ErrLifecycleInvalid
	}
	if r.Action == LifecycleUninstall && r.ProjectAPIKey != "" {
		return ErrLifecycleInvalid
	}
	if len(r.ProjectAPIKey) > 4096 || !utf8.ValidString(r.ProjectAPIKey) || strings.TrimSpace(r.ProjectAPIKey) != r.ProjectAPIKey || strings.ContainsAny(r.ProjectAPIKey, "\r\n\x00") {
		return ErrLifecycleInvalid
	}
	for _, c := range r.ProjectAPIKey {
		if c < 33 || c > 126 {
			return ErrLifecycleInvalid
		}
	}
	return nil
}

// Digest returns SHA-256 of the validated, canonical typed message, including
// the credential when supplied. Persist this digest, never a plaintext body in
// receipts/logs. All v1 identifiers are canonical lowercase UUID strings.
func (r LifecycleRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", ErrLifecycleInvalid
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// LifecycleState is the receiver's durable watermark, scoped by project and
// integration. Keep it after uninstall (a tombstone), without TTL while old
// messages can be replayed. The zero value represents an uninitialized stream.
// It contains no credentials. The installation credential and this state MUST
// be changed in the same serializable transaction.
type LifecycleState struct {
	IntegrationID  string
	ProjectID      string
	InstallationID string
	Sequence       int64
	EventID        string
	Action         string
	RequestDigest  string
}

// LifecycleReceipt acknowledges the exact incoming message only AFTER its
// transaction/required cleanup commits. A bare 200/202 is not an acknowledgement.
type LifecycleReceipt struct {
	Protocol         string `json:"protocol"`
	EventID          string `json:"eventId"`
	IntegrationID    string `json:"integrationId"`
	ProjectID        string `json:"projectId"`
	InstallationID   string `json:"installationId"`
	Sequence         int64  `json:"sequence"`
	RequestDigest    string `json:"requestDigest"`
	Outcome          string `json:"outcome"`
	ObservedSequence int64  `json:"observedSequence"`
}

// LifecycleDecision is a pure transaction plan, NOT a commit or authorization.
// For Applied, install sets/replaces the credential (including clearing it when
// absent); uninstall clears it. Never delete project accounts/payment history.
// Duplicate/Superseded cause no credential or business-state mutation.
type LifecycleDecision struct {
	State   LifecycleState
	Receipt LifecycleReceipt
}

func DecideLifecycle(current LifecycleState, request LifecycleRequest) (LifecycleDecision, error) {
	digest, err := request.Digest()
	if err != nil {
		return LifecycleDecision{}, err
	}
	if err := validateLifecycleState(current); err != nil {
		return LifecycleDecision{}, err
	}
	if current.Sequence != 0 && (current.ProjectID != request.ProjectID || current.IntegrationID != request.IntegrationID) {
		return LifecycleDecision{}, ErrLifecycleConflict
	}
	next := current
	outcome := LifecycleApplied
	switch {
	case request.Sequence < current.Sequence:
		outcome = LifecycleSuperseded
	case request.Sequence == current.Sequence:
		if digest != current.RequestDigest || request.EventID != current.EventID || request.InstallationID != current.InstallationID || request.Action != current.Action {
			return LifecycleDecision{}, ErrLifecycleConflict
		}
		outcome = LifecycleDuplicate
	default:
		// Event IDs are immutable; an immediately repeated ID cannot claim a
		// fresh sequence. Backend also enforces stream-wide event uniqueness.
		if current.EventID == request.EventID {
			return LifecycleDecision{}, ErrLifecycleConflict
		}
		// A closed lifetime can never be reopened, even with a higher sequence.
		if current.InstallationID == request.InstallationID && current.Action == LifecycleUninstall && request.Action == LifecycleInstall {
			return LifecycleDecision{}, ErrLifecycleConflict
		}
		next = LifecycleState{IntegrationID: request.IntegrationID, ProjectID: request.ProjectID, InstallationID: request.InstallationID, Sequence: request.Sequence, EventID: request.EventID, Action: request.Action, RequestDigest: digest}
	}
	return LifecycleDecision{State: next, Receipt: LifecycleReceipt{Protocol: LifecycleProtocol, EventID: request.EventID, IntegrationID: request.IntegrationID, ProjectID: request.ProjectID, InstallationID: request.InstallationID, Sequence: request.Sequence, RequestDigest: digest, Outcome: outcome, ObservedSequence: next.Sequence}}, nil
}

func validateLifecycleState(s LifecycleState) error {
	if s == (LifecycleState{}) {
		return nil
	}
	if s.Sequence < 1 || !lifecycleValidID(s.IntegrationID) || !lifecycleValidID(s.ProjectID) || !lifecycleValidID(s.InstallationID) || !lifecycleValidID(s.EventID) || (s.Action != LifecycleInstall && s.Action != LifecycleUninstall) {
		return ErrLifecycleState
	}
	h, err := hex.DecodeString(s.RequestDigest)
	if err != nil || len(h) != sha256.Size || s.RequestDigest != strings.ToLower(s.RequestDigest) {
		return ErrLifecycleState
	}
	return nil
}

func (r LifecycleReceipt) ValidateFor(request LifecycleRequest) error {
	digest, err := request.Digest()
	if err != nil {
		return err
	}
	if r.Protocol != LifecycleProtocol || r.EventID != request.EventID || r.IntegrationID != request.IntegrationID || r.ProjectID != request.ProjectID || r.InstallationID != request.InstallationID || r.Sequence != request.Sequence || r.RequestDigest != digest {
		return fmt.Errorf("%w: receipt does not identify the request", ErrLifecycleInvalid)
	}
	switch r.Outcome {
	case LifecycleApplied, LifecycleDuplicate:
		if r.ObservedSequence == request.Sequence {
			return nil
		}
	case LifecycleSuperseded:
		if r.ObservedSequence > request.Sequence {
			return nil
		}
	}
	return fmt.Errorf("%w: inconsistent receipt outcome", ErrLifecycleInvalid)
}
