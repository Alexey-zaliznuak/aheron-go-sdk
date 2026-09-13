package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// HandleMigration serves proof and settings on the manifest's migration URL.
// Routing reads only a bounded protocol discriminator; each branch then verifies
// its own signature domain and strictly decodes the entire verified body before I/O.
func (v *Verifier) HandleMigration(receiver *integrationoauth.MigrationReceiver) (http.Handler, error) {
	if v == nil || receiver == nil {
		return nil, integrationoauth.ErrConfig
	}
	makeHandler := func(protocol string) http.Handler {
		return v.verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _, _ := VerifiedBody(r)
			var result any
			var err error
			if protocol == integrationoauth.MigrationProofProtocol {
				c, decodeErr := integrationoauth.DecodeMigrationChallenge(body)
				if decodeErr != nil {
					writeJSONError(w, 400, "invalid migration challenge")
					return
				}
				result, err = receiver.SubmitProof(r.Context(), c)
			} else {
				c, decodeErr := integrationoauth.DecodeMigrationSettings(body)
				if decodeErr != nil {
					writeJSONError(w, 400, "invalid migration settings")
					return
				}
				result, err = receiver.StoreSettings(r.Context(), c)
			}
			if err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, integrationoauth.ErrRequest) {
					status = http.StatusConflict
				}
				writeJSONError(w, status, "migration unavailable")
				return
			}
			writeJSON(w, 200, result)
		}), protocol)
	}
	proof, settings := makeHandler(integrationoauth.MigrationProofProtocol), makeHandler(integrationoauth.MigrationSettingsProtocol)
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
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			writeJSONError(w, 400, "invalid migration request")
			return
		}
		var envelope struct {
			Protocol string `json:"protocol"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			writeJSONError(w, 400, "invalid migration request")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		switch envelope.Protocol {
		case integrationoauth.MigrationProofProtocol:
			proof.ServeHTTP(w, r)
		case integrationoauth.MigrationSettingsProtocol:
			settings.ServeHTTP(w, r)
		default:
			writeJSONError(w, 400, "unsupported migration protocol")
		}
	}), nil
}
