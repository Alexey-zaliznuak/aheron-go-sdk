package campaign

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestStageIntentAndLegacyCompatibility(t *testing.T) {
	valid := StageIntent{IntegrationID: uuid.New(), InstallationID: uuid.New(), SourceGeneration: "1", TemplateID: uuid.New(), TemplateRevision: "9007199254740993", StageKey: "received"}
	if err := ValidateStatusIntent(nil, &valid); err != nil {
		t.Fatal(err)
	}
	legacy := uuid.New()
	if ValidateStatusIntent(&legacy, &valid) == nil {
		t.Fatal("ambiguous legacy and stage intent accepted")
	}
	for _, change := range []func(*StageIntent){
		func(s *StageIntent) { s.IntegrationID = uuid.Nil },
		func(s *StageIntent) { s.InstallationID = uuid.Nil },
		func(s *StageIntent) { s.TemplateRevision = "01" },
		func(s *StageIntent) { s.TemplateRevision = "18446744073709551616" },
		func(s *StageIntent) { s.SourceGeneration = "0" },
		func(s *StageIntent) { s.StageKey = "sent\n" },
		func(s *StageIntent) { s.StageKey = strings.Repeat("я", 65) },
	} {
		invalid := valid
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("invalid frozen intent accepted: %+v", invalid)
		}
	}
	contextRaw, err := json.Marshal(Context{RequestedStatusID: &legacy})
	if err != nil || strings.Contains(string(contextRaw), "requestedStage") {
		t.Fatal("legacy context gained new hash material", err)
	}
	observationRaw, err := json.Marshal(DeliveryObservation{State: "sent", RequestedStatusID: &legacy})
	if err != nil || strings.Contains(string(observationRaw), "requestedStage") {
		t.Fatal("legacy observation gained new hash material", err)
	}
	if ValidateStatusIntent(nil, nil) != nil || ValidateStatusIntent(&legacy, nil) != nil {
		t.Fatal("optional analytics/legacy intent rejected")
	}
	delivery := uuid.New()
	if StageObservationEventID(delivery) != StageObservationEventID(delivery) || StageObservationEventID(delivery) == uuid.NewSHA1(delivery, []byte("requested-status:sent:v1")) {
		t.Fatal("unstable or colliding semantic event identity")
	}
}
