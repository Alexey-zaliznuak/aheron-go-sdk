package campaign

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEventHashAndIdentity(t *testing.T) {
	p, c, s, ec := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e := Event{EventID: uuid.New(), SchemaVersion: SchemaVersion, Source: SourceMessengers, Kind: KindSend, ProjectID: p, CampaignID: c, SubjectID: s, DeliveryID: DeliveryID(ec, 3), SourceEntityID: ec.String(), SourceRevision: 1, OccurredAt: time.Now().UTC().Truncate(time.Microsecond), Payload: json.RawMessage(`{"state":"sent","n":9007199254740993}`)}
	first, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	e.Payload = json.RawMessage(`{ "n":9007199254740993, "state":"sent" }`)
	second, err := e.Hash()
	if err != nil || first != second {
		t.Fatal("JSON order changed event identity", err)
	}
	e.Payload = json.RawMessage(`{"state":"sent","n":9007199254740992}`)
	third, err := e.Hash()
	if err != nil || third == first {
		t.Fatal("large number lost precision", err)
	}
	e.Source = SourceExecution
	if _, err = e.Hash(); err == nil {
		t.Fatal("execution may not author send facts")
	}
}

func TestStableRecipientBucketAndDelivery(t *testing.T) {
	p, s, ec := uuid.New(), uuid.New(), uuid.New()
	if Bucket(p, s) >= 64 || Bucket(p, s) != Bucket(p, s) {
		t.Fatal("unstable bucket")
	}
	if DeliveryID(ec, 1) == DeliveryID(ec, 2) || DeliveryID(ec, 1) != DeliveryID(ec, 1) {
		t.Fatal("delivery identity reused or unstable")
	}
	if SystemStatusID(p, "received") == SystemStatusID(p, "not_received") {
		t.Fatal("status identity collision")
	}
}
