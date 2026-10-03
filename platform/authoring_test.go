package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthoringPreservesIdentityCommandAndSingleAttempt(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	rawToken := testToken("user")
	count := 0
	fail := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer "+rawToken {
			t.Error("wrong identity")
		}
		if fail {
			http.Error(w, "SECRET token", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "native-blocks/prepare") {
			fmt.Fprint(w, `{"type":"delay","settings":{"mode":"constant","amount":"10","unit":"minutes"}}`)
			return
		}
		var command GraphCommand
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Error(err)
		}
		if command.OperationID != id || command.BaseRevision != 7 || command.Edits[0].Action != "create" {
			t.Errorf("changed command: %+v", command)
		}
		fmt.Fprintf(w, `{"schemeId":%q,"operationId":%q,"revision":8}`, id, id)
	}))
	defer api.Close()
	c, err := New(Config{BaseURL: api.URL, AllowLoopbackHTTP: true, TokenProvider: provider(t, rawToken)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := GraphCommand{OperationID: id, EditorSessionID: id, BaseRevision: 7, CommandType: "edit", Edits: []GraphEdit{{EntityKind: "step", Action: "create", Data: json.RawMessage(`{"type":"note","settings":{"text":"hello"}}`)}}}
	if _, err := c.Schemes.ApplyGraphCommand(context.Background(), id, id, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schemes.PrepareNativeBlock(context.Background(), id, "delay", json.RawMessage(`{"amount":"10","unit":"minutes"}`)); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := c.Schemes.ApplyGraphCommand(context.Background(), id, id, cmd); !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaked or lost status: %v", err)
	}
	if count != 3 {
		t.Fatalf("write retried: %d", count)
	}
	cmd.OperationID = "../wrong"
	if _, err := c.Schemes.ApplyGraphCommand(context.Background(), id, id, cmd); !errors.Is(err, ErrInvalidInput) || count != 3 {
		t.Fatalf("invalid request reached network: %v", err)
	}
}
