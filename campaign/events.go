// Package campaign defines the durable observations shared by campaign,
// execution and messenger services. It contains no transport or database code.
package campaign

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	SchemaVersion        uint32 = 1
	DefaultTopic                = "aheron.integrations.messengers.campaign.events"
	SourceCampaign              = "campaign"
	SourceExecution             = "execution"
	SourceMessengers            = "messengers"
	KindAudiencePage            = "audience.page"
	KindSnapshotComplete        = "audience.complete"
	KindDispatch                = "delivery.dispatch"
	KindSend                    = "delivery.send"
	KindResolve                 = "delivery.resolve"
)

// Event identity, source revision, occurrence time and payload are immutable.
// Retries republish the exact same event. A reused ID with a different hash is
// an integrity error, never an update. SubjectID is zero for audience envelopes.
type Event struct {
	EventID        uuid.UUID       `json:"eventId"`
	SchemaVersion  uint32          `json:"schemaVersion"`
	Source         string          `json:"source"`
	Kind           string          `json:"kind"`
	ProjectID      uuid.UUID       `json:"projectId"`
	CampaignID     uuid.UUID       `json:"campaignId"`
	SubjectID      uuid.UUID       `json:"subjectId,omitempty"`
	DeliveryID     uuid.UUID       `json:"deliveryId,omitempty"`
	SourceEntityID string          `json:"sourceEntityId"`
	SourceRevision uint64          `json:"sourceRevision"`
	OccurredAt     time.Time       `json:"occurredAt"`
	Payload        json.RawMessage `json:"payload"`
}

type AudiencePage struct {
	CampaignCreatedAt   time.Time   `json:"campaignCreatedAt"`
	SubjectIDs          []uuid.UUID `json:"subjectIds"`
	PreparationRequired bool        `json:"preparationRequired,omitempty"`
}

type SnapshotComplete struct {
	CampaignCreatedAt   time.Time `json:"campaignCreatedAt"`
	ExpectedRecipients  uint64    `json:"expectedRecipients"`
	PreparationRequired bool      `json:"preparationRequired,omitempty"`
}

// Context is captured from the trusted execution envelope when the command is
// accepted. Do not reconstruct it from a context that may have been restarted.
type Context struct {
	CampaignID         uuid.UUID    `json:"campaignId"`
	SubjectID          uuid.UUID    `json:"subjectId"`
	DeliveryID         uuid.UUID    `json:"deliveryId"`
	ExecutionContextID uuid.UUID    `json:"executionContextId"`
	ContextVersion     int64        `json:"contextVersion"`
	SchemeID           *uuid.UUID   `json:"schemeId,omitempty"`
	StepID             *uuid.UUID   `json:"stepId,omitempty"`
	RequestedStatusID  *uuid.UUID   `json:"requestedStatusId,omitempty"`
	RequestedStage     *StageIntent `json:"requestedStage,omitempty"`
}

// DeliveryObservation changes exactly the dimension selected by Event.Kind.
// State/revision of dispatch, send and resolve must never overwrite one another.
// ErrorClass is a bounded classifier; ErrorCode is a platform code, never a
// reflected error body. SubjectID in the envelope is the scenario participant;
// RecipientSubjectID identifies the actual addressee (which may be different).
type DeliveryObservation struct {
	ExecutionContextID  uuid.UUID    `json:"executionContextId"`
	ContextVersion      int64        `json:"contextVersion"`
	SchemeID            *uuid.UUID   `json:"schemeId,omitempty"`
	StepID              *uuid.UUID   `json:"stepId,omitempty"`
	ChannelID           *uuid.UUID   `json:"channelId,omitempty"`
	DialogID            *uuid.UUID   `json:"dialogId,omitempty"`
	MessageID           *uuid.UUID   `json:"messageId,omitempty"`
	PeerID              *uuid.UUID   `json:"peerId,omitempty"`
	RecipientSubjectID  *uuid.UUID   `json:"recipientSubjectId,omitempty"`
	RecipientKind       string       `json:"recipientKind,omitempty"`
	ExternalRecipientID string       `json:"externalRecipientId,omitempty"`
	Platform            string       `json:"platform,omitempty"`
	RequestedStatusID   *uuid.UUID   `json:"requestedStatusId,omitempty"`
	RequestedStage      *StageIntent `json:"requestedStage,omitempty"`
	State               string       `json:"state"`
	ErrorClass          string       `json:"errorClass,omitempty"`
	ErrorCode           string       `json:"errorCode,omitempty"`
	PlatformAttempts    uint32       `json:"platformAttempts,omitempty"`
	SentPartCount       uint32       `json:"sentPartCount,omitempty"`
	ExternalMessageIDs  []string     `json:"externalMessageIds,omitempty"`
	QueuedAt            *time.Time   `json:"queuedAt,omitempty"`
	SentAt              *time.Time   `json:"sentAt,omitempty"`
	FinishedAt          *time.Time   `json:"finishedAt,omitempty"`
}

func DeliveryID(executionContextID uuid.UUID, contextVersion int64) uuid.UUID {
	return uuid.NewSHA1(executionContextID, []byte("send-message:"+strconv.FormatInt(contextVersion, 10)))
}

func AudienceSubjectEventID(sourceEventID, subjectID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(sourceEventID, []byte(subjectID.String()))
}

func ObservationEventID(deliveryID uuid.UUID, kind string, revision uint64) uuid.UUID {
	return uuid.NewSHA1(deliveryID, []byte(kind+":"+strconv.FormatUint(revision, 10)))
}

func Bucket(projectID, subjectID uuid.UUID) uint8 {
	h := sha256.Sum256([]byte(projectID.String() + ":" + subjectID.String()))
	return h[0] & 63
}

func SystemStatusID(projectID uuid.UUID, systemKey string) uuid.UUID {
	return uuid.NewSHA1(projectID, []byte("messengers:campaign-status:"+systemKey))
}

// Hash normalizes JSON object order without rounding numeric values and includes
// the complete envelope. It never includes consumer application markers.
func (e Event) Hash() ([32]byte, error) {
	if err := e.Validate(); err != nil {
		return [32]byte{}, err
	}
	canonical, err := canonicalPayload(e.Payload)
	if err != nil {
		return [32]byte{}, err
	}
	e.Payload = canonical
	e.OccurredAt = e.OccurredAt.UTC()
	raw, err := json.Marshal(e)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion || e.EventID == uuid.Nil || e.ProjectID == uuid.Nil || e.CampaignID == uuid.Nil || e.SourceEntityID == "" || e.SourceRevision == 0 || e.OccurredAt.IsZero() {
		return fmt.Errorf("campaign: invalid event identity")
	}
	if !json.Valid(e.Payload) || len(e.Payload) > 1<<20 {
		return fmt.Errorf("campaign: invalid event payload")
	}
	switch e.Kind {
	case KindAudiencePage, KindSnapshotComplete:
		if e.Source != SourceCampaign || e.DeliveryID != uuid.Nil {
			return fmt.Errorf("campaign: invalid audience source")
		}
	case KindDispatch:
		if e.Source != SourceExecution || e.SubjectID == uuid.Nil || e.DeliveryID == uuid.Nil {
			return fmt.Errorf("campaign: invalid dispatch source")
		}
	case KindSend, KindResolve:
		if e.Source != SourceMessengers || e.SubjectID == uuid.Nil || e.DeliveryID == uuid.Nil {
			return fmt.Errorf("campaign: invalid delivery source")
		}
	default:
		return fmt.Errorf("campaign: unsupported event kind")
	}
	return nil
}
