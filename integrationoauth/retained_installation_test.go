package integrationoauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestDecodeRetainedInstallationIdentityIsStrictAndHashesAllSettings(t *testing.T) {
	ids := []string{
		"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666",
		"77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888",
	}
	digestBytes := sha256.Sum256([]byte("digest"))
	digest := hex.EncodeToString(digestBytes[:])
	settings := map[string]any{
		"protocol": retainedInstallationSettingsProtocol, "commandId": ids[0], "integrationId": ids[1], "jobId": ids[2],
		"projectId": ids[3], "installationId": ids[4], "clientId": ids[5], "keyId": "kid",
		"legacyKeyId": ids[6], "proofId": ids[7], "proofDigest": digest,
		"accessVersion": 1, "integrationAccessVersion": 2, "grantVersion": 3, "policyRevision": 4, "policyDigest": digest,
	}
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := json.Marshal(map[string]any{"pending": false, "legacyKeyDigest": digest, "settings": json.RawMessage(settingsRaw)})
	if err != nil {
		t.Fatal(err)
	}
	decoded, revision, err := DecodeRetainedInstallationIdentity(outer)
	if err != nil || decoded == nil || revision == "" {
		t.Fatalf("decode retained identity: settings=%+v revision=%q err=%v", decoded, revision, err)
	}
	if decoded.ClientID != ids[5] || decoded.KeyID != "kid" || decoded.PolicyRevision != 4 {
		t.Fatalf("decoded settings lost identity: %+v", decoded)
	}

	settings["clientId"] = ids[0]
	changed, _ := json.Marshal(settings)
	outerChanged, _ := json.Marshal(map[string]any{"pending": false, "legacyKeyDigest": digest, "settings": json.RawMessage(changed)})
	_, changedRevision, err := DecodeRetainedInstallationIdentity(outerChanged)
	if err != nil || changedRevision == revision {
		t.Fatalf("settings identity change did not change revision: old=%q new=%q err=%v", revision, changedRevision, err)
	}
	if _, _, err := DecodeRetainedInstallationIdentity([]byte(`{"pending":false,"legacyKeyDigest":"` + digest + `","settings":{"unknown":true}}`)); err == nil {
		t.Fatal("unknown retained field was accepted")
	}
}
