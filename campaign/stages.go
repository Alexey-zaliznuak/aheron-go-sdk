package campaign

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// InitialStageSourceGeneration is an analytics projection fence, not an installation version.
const InitialStageSourceGeneration = "1"

// StageIntent freezes explicit user intent. IntegrationID supplies stable source
// ownership; InstallationID records the accepting installation for audit only.
// Both identities and SourceGeneration come from trusted context, never settings.
type StageIntent struct {
	IntegrationID    uuid.UUID `json:"integrationId"`
	InstallationID   uuid.UUID `json:"installationId"`
	SourceGeneration string    `json:"sourceGeneration"`
	TemplateID       uuid.UUID `json:"templateId"`
	TemplateRevision string    `json:"templateRevision"`
	StageKey         string    `json:"stageKey"`
}

func (s StageIntent) Validate() error {
	if s.IntegrationID == uuid.Nil || s.InstallationID == uuid.Nil || s.TemplateID == uuid.Nil || !positiveRevision(s.SourceGeneration) || !positiveRevision(s.TemplateRevision) || !utf8.ValidString(s.StageKey) || len(s.StageKey) == 0 || len(s.StageKey) > 128 || strings.TrimSpace(s.StageKey) != s.StageKey || strings.IndexFunc(s.StageKey, unicode.IsControl) >= 0 {
		return errors.New("campaign: invalid stage intent")
	}
	return nil
}
func positiveRevision(v string) bool {
	n, e := strconv.ParseUint(v, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == v
}

// ValidateStatusIntent rejects ambiguous legacy and new stage intentions.
func ValidateStatusIntent(legacy *uuid.UUID, stage *StageIntent) error {
	if legacy != nil && (*legacy == uuid.Nil || stage != nil) {
		return errors.New("campaign: ambiguous or invalid status intent")
	}
	if stage != nil {
		return stage.Validate()
	}
	return nil
}

func StageObservationEventID(deliveryID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(deliveryID, []byte("requested-stage:sent:v1"))
}
