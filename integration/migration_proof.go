package integration

import (
	"context"
	"errors"
	"mime"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// ReadMigrationCredential reads the existing installation's current legacy key.
// It MUST reject missing/deleted installations and, when already known, a
// different installationId. It performs no upsert, identity binding or activation.
// Auth independently checks the key's exact ID, ownership and current backend
// lifetime. A late challenge therefore cannot resurrect an installation.
type ReadMigrationCredential func(context.Context, integrationoauth.MigrationChallenge) (string, error)

// HandleMigrationProof serves a dedicated migration endpoint with its own signed
// domain, separate from install/uninstall/lifecycle. Endpoint publication and
// durable receiver identity confirmation belong to the integration's rollout.
func (v *Verifier) HandleMigrationProof(client *integrationoauth.MigrationProofClient, read ReadMigrationCredential) (http.Handler, error) {
	if v == nil || client == nil || read == nil {
		return nil, integrationoauth.ErrConfig
	}
	verified := v.verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _, _ := VerifiedBody(r)
		challenge, err := integrationoauth.DecodeMigrationChallenge(body)
		if err != nil {
			writeJSONError(w, 400, "invalid migration challenge")
			return
		}
		if !client.Accepts(challenge) {
			writeJSONError(w, 409, "migration challenge unavailable")
			return
		}
		key, err := read(r.Context(), challenge)
		if err != nil || key == "" {
			status := http.StatusServiceUnavailable
			if errors.Is(err, integrationoauth.ErrRequest) || err == nil {
				status = http.StatusConflict
			}
			writeJSONError(w, status, "migration credential unavailable")
			return
		}
		receipt, err := client.Submit(r.Context(), challenge, key)
		if err != nil {
			writeJSONError(w, 503, "migration proof unavailable")
			return
		}
		writeJSON(w, 200, receipt)
	}), integrationoauth.MigrationProofProtocol)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSONError(w, 405, "POST required")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Authorization") != "" {
			writeJSONError(w, 400, "invalid migration request")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
		verified.ServeHTTP(w, r)
	}), nil
}
