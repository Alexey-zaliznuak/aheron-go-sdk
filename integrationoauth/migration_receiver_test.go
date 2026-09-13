package integrationoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type receiverMemoryStore struct {
	mu    sync.Mutex
	state MigrationInstallation
	lost  bool
	hook  func(*MigrationInstallation)
}

func (s *receiverMemoryStore) ReadMigrationInstallation(context.Context, string) (MigrationInstallation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMigrationInstallation(s.state), nil
}
func (s *receiverMemoryStore) CompareAndSwapMigrationInstallation(_ context.Context, before, after MigrationInstallation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hook != nil {
		s.hook(&s.state)
		s.hook = nil
	}
	if !reflect.DeepEqual(s.state, before) {
		return ErrRequest
	}
	s.state = cloneMigrationInstallation(after)
	if s.lost {
		s.lost = false
		return errors.New("lost commit response")
	}
	return nil
}

func settingsFixture(c MigrationChallenge) MigrationSettingsRequest {
	return MigrationSettingsRequest{Protocol: MigrationSettingsProtocol, CommandID: c.ProofID, IntegrationID: c.IntegrationID, JobID: c.JobID, ProjectID: c.ProjectID, InstallationID: c.InstallationID, ClientID: c.ClientID, KeyID: c.KeyID, LegacyKeyID: c.LegacyKeyID, ProofID: c.ProofID, ProofDigest: c.RequestDigest, AccessVersion: 2, IntegrationAccessVersion: 1, GrantVersion: 1, PolicyRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
}

func TestMigrationReceiverRecovery(t *testing.T) {
	for _, mode := range []string{"success", "lost auth reply", "lost proof commit", "lost settings commit", "reinstall before proof commit", "reinstall before settings commit", "changed key", "disabled", "conflicting command", "rollback version"} {
		t.Run(mode, func(t *testing.T) {
			key, ch := proofFixture()
			store := &receiverMemoryStore{state: MigrationInstallation{IntegrationID: ch.IntegrationID, ProjectID: ch.ProjectID, Generation: "local-lifetime-1", Active: true, LegacyAPIKey: "private-existing-key"}}
			var calls atomic.Int32
			auth := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				store.mu.Lock()
				persisted := store.state.Migration.Pending != nil
				store.mu.Unlock()
				if !persisted {
					t.Error("auth called before durable challenge binding")
				}
				if mode == "lost auth reply" {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(proofResponse(ch))
			}))
			defer auth.Close()
			client, err := NewMigrationProofClient(MigrationProofConfig{IntegrationID: ch.IntegrationID, PrivateKey: key, ProofEndpoint: auth.URL + "/proof", HTTPClient: auth.Client()})
			if err != nil {
				t.Fatal(err)
			}
			receiver, _ := NewMigrationReceiver(client, store)
			if mode == "lost proof commit" {
				store.lost = true
			}
			if mode == "reinstall before proof commit" {
				store.hook = func(s *MigrationInstallation) { s.Generation = "local-lifetime-2" }
			}
			_, err = receiver.SubmitProof(context.Background(), ch)
			if mode == "reinstall before proof commit" {
				if err == nil || calls.Load() != 0 || store.state.Migration.Pending != nil {
					t.Fatal("reinstalled row bound", err)
				}
				return
			}
			if mode == "lost proof commit" {
				if err == nil || calls.Load() != 0 {
					t.Fatal("unknown commit reached auth")
				}
				_, err = receiver.SubmitProof(context.Background(), ch)
			}
			if mode != "lost auth reply" && err != nil {
				t.Fatal(err)
			}
			if store.state.InstallationID != "" {
				t.Fatal("proof changed active identity")
			}
			raw, _ := json.Marshal(store.state.Migration)
			if strings.Contains(string(raw), store.state.LegacyAPIKey) || strings.Contains(string(raw), ch.Nonce) {
				t.Fatal("secret persisted in migration state")
			}
			c := settingsFixture(ch)
			switch mode {
			case "lost settings commit":
				store.lost = true
			case "reinstall before settings commit":
				store.hook = func(s *MigrationInstallation) { s.Generation = "local-lifetime-2" }
			case "changed key":
				store.state.LegacyAPIKey = "new-key"
			case "disabled":
				store.state.Active = false
			}
			out, err := receiver.StoreSettings(context.Background(), c)
			if mode == "reinstall before settings commit" || mode == "changed key" || mode == "disabled" {
				if err == nil || store.state.Migration.Settings != nil {
					t.Fatal("unsafe settings saved")
				}
				return
			}
			if mode == "lost settings commit" {
				if err == nil || store.state.Migration.Settings == nil {
					t.Fatal("commit loss not exercised")
				}
			}
			// New receiver instance reads durable state after a process restart.
			receiver, _ = NewMigrationReceiver(client, store)
			if mode == "conflicting command" {
				c.PolicyDigest = strings.Repeat("b", 64)
			}
			if mode == "rollback version" {
				c.AccessVersion--
			}
			out, err = receiver.StoreSettings(context.Background(), c)
			if mode == "conflicting command" || mode == "rollback version" {
				if err == nil {
					t.Fatal("changed command accepted")
				}
				return
			}
			if err != nil || !out.Matches(c) || store.state.InstallationID != c.InstallationID || store.state.Migration.Settings == nil {
				t.Fatal("settings not recovered", err)
			}
		})
	}
}

func TestMigrationSettingsStrictContract(t *testing.T) {
	_, ch := proofFixture()
	c := settingsFixture(ch)
	raw, _ := json.Marshal(c)
	if got, err := DecodeMigrationSettings(raw); err != nil || got.Digest() != c.Digest() {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"protocol":`, `"Protocol":`, 1),
		strings.Replace(string(raw), `"accessVersion":2`, `"accessVersion":null`, 1),
		strings.Replace(string(raw), `"accessVersion":2`, `"accessVersion":2,"accessVersion":3`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"tokenEndpoint":"https://evil.example"}`,
		string(raw) + `{}`,
	} {
		if _, err := DecodeMigrationSettings([]byte(bad)); err == nil {
			t.Fatal("accepted ambiguous settings")
		}
	}
	if (MigrationSettingsReceipt{State: "ready"}).Matches(c) {
		t.Fatal("generic readiness accepted")
	}
}
