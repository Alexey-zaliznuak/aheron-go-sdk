// knowledge-publish is a CI adapter for the documentation SDK. It reads only
// its explicit publishing credential and prints the final receipt, never secrets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"time"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
	"go.uber.org/zap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		log, _ := zap.NewProduction()
		log.Error("knowledge publication failed", zap.String("service", "knowledge-publish"), zap.String("env", "ci"), zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("knowledge-publish", flag.ContinueOnError)
	base := fs.String("base-url", "", "documentation API prefix (HTTPS)")
	path := fs.String("package", "", "canonical package JSON file")
	op := fs.String("operation-id", "", "stable operation ID, e.g. pipeline-123 (reuse on retry)")
	phase := fs.String("phase", "publish", "upload before deployment; activate after readiness; publish combines both")
	current := fs.Bool("current", false, "also promote the current channel (integration providers only)")
	sequence := fs.Int64("release-sequence", 0, "increasing provider release number, unchanged on retry; required with DOCUMENTATION_API_KEY")
	timeout := fs.Duration("timeout", time.Minute, "total publication deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *phase != "upload" && *phase != "activate" && *phase != "publish" {
		return errors.New("phase must be upload, activate or publish")
	}
	if fs.NArg() != 0 || *path == "" || (*phase != "upload" && *op == "") || *timeout <= 0 {
		return errors.New("package and positive timeout required; activation requires operation-id; no positional arguments")
	}
	token, apiKey, err := publicationCredential(os.Getenv("DOCUMENTATION_API_KEY"), os.Getenv("DOCUMENTATION_ID_TOKEN"), *sequence)
	if err != nil {
		return err
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := readPackage(f)
	if err != nil {
		return err
	}
	cfg := docs.Config{BaseURL: *base, PublisherToken: func(context.Context) (string, error) { return token, nil }}
	if apiKey {
		cfg.Publication = &docs.PublicationContext{ProviderKey: p.ProviderKey, SourceRevision: p.SourceRevision, ReleaseSequence: *sequence}
	}
	client, err := docs.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	receipt, err := publishPhase(ctx, client, *phase, p, *op, *current)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
}

func publicationCredential(apiKey, oidc string, sequence int64) (string, bool, error) {
	if apiKey != "" && oidc != "" {
		return "", false, errors.New("set only one of DOCUMENTATION_API_KEY and DOCUMENTATION_ID_TOKEN")
	}
	if apiKey != "" {
		if sequence < 1 {
			return "", false, errors.New("positive release-sequence is required with DOCUMENTATION_API_KEY")
		}
		return apiKey, true, nil
	}
	if oidc == "" {
		return "", false, errors.New("DOCUMENTATION_API_KEY or DOCUMENTATION_ID_TOKEN is required")
	}
	if sequence != 0 {
		return "", false, errors.New("release-sequence must be omitted with DOCUMENTATION_ID_TOKEN")
	}
	return oidc, false, nil
}

func publishPhase(ctx context.Context, client *docs.Client, phase string, p docs.Package, operationID string, current bool) (any, error) {
	if _, ok := docs.IntegrationProviderID(p.ProviderKey); current && !ok {
		return nil, errors.New("current publication requires an integration provider")
	}
	switch phase {
	case "upload":
		return client.Upload(ctx, p)
	case "activate":
		if current {
			return client.ActivateCurrentPackage(ctx, p, operationID)
		}
		return client.ActivatePackage(ctx, p, operationID)
	case "publish":
		if current {
			return client.PublishCurrent(ctx, p, operationID)
		}
		return client.Publish(ctx, p, operationID)
	default:
		return nil, errors.New("unknown publication phase")
	}
}

func readPackage(r io.Reader) (docs.Package, error) {
	raw, err := io.ReadAll(io.LimitReader(r, docs.MaxPackageBytes+1))
	if err != nil {
		return docs.Package{}, err
	}
	if len(raw) > docs.MaxPackageBytes {
		return docs.Package{}, errors.New("documentation package too large")
	}
	var p docs.Package
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return docs.Package{}, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return docs.Package{}, errors.New("expected exactly one documentation package")
	}
	p, _, err = docs.CanonicalPackage(p)
	return p, err
}
