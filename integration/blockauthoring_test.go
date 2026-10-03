package integration

import (
	"encoding/json"
	"testing"
)

func TestAuthoringPatchPreservesUnknownFieldsAndExactNumbers(t *testing.T) {
	base := json.RawMessage(`{"text":"old","future":{"id":9007199254740993},"options":{"pinMessage":true,"disableLinkPreview":true},"keyboard":[{"id":"stable"}],"waitReply":{}}`)
	result, err := AuthoringSettings(BlockAuthoringRequest{BaseSettings: base, Changes: json.RawMessage(`{"text":"new","options":{"pinMessage":false},"waitReply":null}`)})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(result, &fields)
	if string(fields["future"]) != `{"id":9007199254740993}` || string(fields["keyboard"]) != `[{"id":"stable"}]` || fields["waitReply"] != nil || string(fields["options"]) != `{"disableLinkPreview":true,"pinMessage":false}` {
		t.Fatalf("lost settings: %s", result)
	}
	if string(base) != `{"text":"old","future":{"id":9007199254740993},"options":{"pinMessage":true,"disableLinkPreview":true},"keyboard":[{"id":"stable"}],"waitReply":{}}` {
		t.Fatal("mutated original settings")
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"options":`} {
		if _, err := AuthoringSettings(BlockAuthoringRequest{BaseSettings: base, Changes: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted invalid patch %s", raw)
		}
	}
}
