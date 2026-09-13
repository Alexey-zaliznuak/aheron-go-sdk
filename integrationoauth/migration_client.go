package integrationoauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const MigrationProofProtocol = "aheron.oauth-migration-challenge.v1"

var ErrMigrationProof = errors.New("integration OAuth: migration proof failed")

type MigrationChallenge struct {
	Protocol string `json:"protocol"`
	MigrationProofRequest
	RequestDigest string `json:"requestDigest"`
}

func (MigrationChallenge) String() string   { return "[redacted migration challenge]" }
func (MigrationChallenge) GoString() string { return "[redacted migration challenge]" }
func (c MigrationChallenge) Valid() bool {
	return c.Protocol == MigrationProofProtocol && c.MigrationProofRequest.Valid() && c.RequestDigest == c.Digest()
}

type MigrationProofConfig struct {
	IntegrationID string
	PrivateKey    ed25519.PrivateKey
	// Pinned deployment configuration. Never accept an issuer URL in a command.
	ProofEndpoint string
	HTTPClient    *http.Client
}

type MigrationProofClient struct {
	integrationID, endpoint, keyID string
	key                            ed25519.PrivateKey
	http                           *http.Client
	now                            func() time.Time
}

func (*MigrationProofClient) String() string   { return "[redacted migration proof client]" }
func (*MigrationProofClient) GoString() string { return "[redacted migration proof client]" }

func NewMigrationProofClient(cfg MigrationProofConfig) (*MigrationProofClient, error) {
	u, err := url.Parse(cfg.ProofEndpoint)
	if !canonicalUUID(cfg.IntegrationID) || len(cfg.PrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(cfg.PrivateKey, ed25519.NewKeyFromSeed(cfg.PrivateKey.Seed())) || err != nil || !httpsURL(u) || u.Path == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.ProofEndpoint, "#") || len(cfg.ProofEndpoint) > 2048 {
		return nil, ErrConfig
	}
	client := boundedClient(cfg.HTTPClient)
	if client.Timeout > 30*time.Second {
		return nil, ErrConfig
	}
	h := sha256.Sum256(cfg.PrivateKey.Public().(ed25519.PublicKey))
	return &MigrationProofClient{integrationID: cfg.IntegrationID, endpoint: cfg.ProofEndpoint, keyID: "migration-" + hex.EncodeToString(h[:]), key: slices.Clone(cfg.PrivateKey), http: client, now: time.Now}, nil
}

func (c *MigrationProofClient) Accepts(ch MigrationChallenge) bool {
	return c != nil && ch.Valid() && ch.IntegrationID == c.integrationID && ch.KeyID == c.keyID && !ch.CreatedAt.After(c.now()) && c.now().Before(ch.ExpiresAt)
}

// Submit proves possession only. It never installs, persists an OAuth identity,
// enables access or obtains an access token. Caller must read the CURRENT legacy
// credential from an existing installation; absent/deleted rows must be rejected.
// Each attempt signs a fresh jti for the same challenge. No automatic HTTP retry.
func (c *MigrationProofClient) Submit(ctx context.Context, ch MigrationChallenge, legacyKey string) (MigrationProofReceipt, error) {
	if !c.Accepts(ch) || !asciiIdentifier(legacyKey, 512) {
		return MigrationProofReceipt{}, ErrRequest
	}
	if err := ctx.Err(); err != nil {
		return MigrationProofReceipt{}, err
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return MigrationProofReceipt{}, ErrMigrationProof
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": c.keyID})
	at := c.now()
	exp := min(at.Unix()+60, ch.ExpiresAt.Unix())
	if exp <= at.Unix() {
		return MigrationProofReceipt{}, ErrRequest
	}
	claims, _ := json.Marshal(map[string]any{"iss": ch.ClientID, "sub": ch.ClientID, "aud": c.endpoint, "iat": at.Unix(), "exp": exp, "jti": base64.RawURLEncoding.EncodeToString(entropy[:]), "proofId": ch.ProofID, "jobId": ch.JobID, "projectId": ch.ProjectID, "installationId": ch.InstallationID, "legacyKeyId": ch.LegacyKeyID, "requestDigest": ch.RequestDigest, "nonce": ch.Nonce, "clientVersion": ch.ClientVersion, "credentialVersion": ch.CredentialVersion})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	assertion := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.key, []byte(input)))
	body, _ := json.Marshal(struct {
		ClientID        string `json:"clientId"`
		ProofID         string `json:"proofId"`
		Nonce           string `json:"nonce"`
		ClientAssertion string `json:"clientAssertion"`
		LegacyAPIKey    string `json:"legacyApiKey"`
	}{ch.ClientID, ch.ProofID, ch.Nonce, assertion, legacyKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return MigrationProofReceipt{}, ErrConfig
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return MigrationProofReceipt{}, migrationTransportError(ctx)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return MigrationProofReceipt{}, ErrMigrationProof
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return MigrationProofReceipt{}, ErrMigrationProof
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 20<<10+1))
	if err != nil {
		return MigrationProofReceipt{}, migrationTransportError(ctx)
	}
	var receipt MigrationProofReceipt
	if decodeMigrationObject(raw, &receipt, migrationReceiptFields) != nil || !receipt.Matches(ch.MigrationProofRequest) || receipt.State != "claimed" || !c.now().Before(ch.ExpiresAt) {
		return MigrationProofReceipt{}, ErrMigrationProof
	}
	return receipt, nil
}

func migrationTransportError(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrMigrationProof, ctx.Err())
	}
	return ErrMigrationProof
}

var migrationRequestFields = []string{"clientId", "proofId", "integrationId", "jobId", "projectId", "installationId", "legacyKeyId", "clientVersion", "credentialVersion", "keyId", "nonce", "expectedKeyUpdatedAt", "sourceScopes", "sourceExpiresAt", "createdAt", "expiresAt", "protocol", "requestDigest"}
var migrationReceiptFields = []string{"clientId", "proofId", "integrationId", "jobId", "projectId", "installationId", "legacyKeyId", "clientVersion", "credentialVersion", "keyId", "requestDigest", "state", "expectedKeyUpdatedAt", "sourceScopes", "sourceExpiresAt", "createdAt", "expiresAt"}

func DecodeMigrationChallenge(raw []byte) (MigrationChallenge, error) {
	var ch MigrationChallenge
	if decodeMigrationObject(raw, &ch, migrationRequestFields) != nil || !ch.Valid() {
		return MigrationChallenge{}, ErrRequest
	}
	return ch, nil
}

func decodeMigrationObject(raw []byte, dst any, fields []string) error {
	if len(raw) > 20<<10 || !utf8.Valid(raw) {
		return ErrRequest
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return ErrRequest
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || !slices.Contains(fields, name) {
			return ErrRequest
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || name != "sourceExpiresAt" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrRequest
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return ErrRequest
	}
	if d.Decode(new(any)) != io.EOF || len(seen) != len(fields) || json.Unmarshal(raw, dst) != nil {
		return ErrRequest
	}
	return nil
}
