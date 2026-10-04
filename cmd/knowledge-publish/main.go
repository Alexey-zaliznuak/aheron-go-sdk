// knowledge-publish is a CI adapter for the documentation SDK. It never reads
// service configuration or user credentials, and prints only the final receipt.
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
	timeout := fs.Duration("timeout", time.Minute, "total publication deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *path == "" || *op == "" || *timeout <= 0 {
		return errors.New("package, operation-id and positive timeout required; no positional arguments")
	}
	token := os.Getenv("DOCUMENTATION_ID_TOKEN")
	if token == "" {
		return errors.New("DOCUMENTATION_ID_TOKEN is required")
	}
	client, err := docs.New(docs.Config{BaseURL: *base, PublisherToken: func(context.Context) (string, error) { return token, nil }})
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
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	receipt, err := client.Publish(ctx, p, *op)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
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
