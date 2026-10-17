// Package executionevent defines integration-neutral runtime observations.
package executionevent

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"time"
)

const DefaultActionTopic = "aheron.execution.integration.action.events"
const SchemaVersion uint32 = 1

// ActionOutcome snapshots identity and generic metadata at dispatch creation.
// Consumers deduplicate EventID. Kafka retention bounds historical recovery.
type ActionOutcome struct {
	EventID            uuid.UUID       `json:"eventId"`
	SchemaVersion      uint32          `json:"schemaVersion"`
	ProjectID          uuid.UUID       `json:"projectId"`
	SubjectID          uuid.UUID       `json:"subjectId"`
	ExecutionContextID uuid.UUID       `json:"executionContextId"`
	ContextVersion     int64           `json:"contextVersion"`
	IntegrationID      uuid.UUID       `json:"integrationId"`
	BlockKey           string          `json:"blockKey"`
	SchemeID           uuid.UUID       `json:"schemeId"`
	StepID             uuid.UUID       `json:"stepId"`
	Outcome            string          `json:"outcome"`
	ErrorClass         string          `json:"errorClass,omitempty"`
	CampaignID         *uuid.UUID      `json:"campaignId,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	OccurredAt         time.Time       `json:"occurredAt"`
	SourceRevision     uint64          `json:"sourceRevision"`
}

func ID(contextID uuid.UUID, version int64, outcome string) uuid.UUID {
	return uuid.NewSHA1(contextID, []byte(fmt.Sprintf("integration-action:%d:%s", version, outcome)))
}
