package transfercrm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

func TestProvisionTransport(t *testing.T) {
	const project = "10000000-0000-4000-8000-000000000001"
	const operation = "20000000-0000-4000-8000-000000000001"
	input := schemetransfer.ProvisionRequest{Kind: "tag", Action: "create", Key: "paid", Name: "Paid"}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"success", 200, `{"kind":"tag","id":"30000000-0000-4000-8000-000000000001","key":"paid","created":true}`, nil},
		{"wrong key", 200, `{"kind":"tag","id":"30000000-0000-4000-8000-000000000001","key":"other","created":true}`, ErrUnavailable},
		{"duplicates", 200, `{"kind":"tag","kind":"tag"}`, ErrUnavailable},
		{"oversized", 200, strings.Repeat(" ", 8193), ErrUnavailable},
		{"conflict", 409, "secret error text", ErrConflict},
		{"not found", 404, "secret error text", ErrNotFound},
		{"bad input", 400, "secret error text", ErrInvalidRequest},
		{"dependency", 500, "secret error text", ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.Path != "/internal/projects/"+project+"/scheme-imports/"+operation+"/resources/t0" || r.Header.Get("X-Internal-Token") != "private-token" {
					t.Error("invalid internal request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "private-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.ProvisionResource(context.Background(), project, operation, "t0", input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("result: %+v error: %v", result, err)
			}
			if err != nil && result.ID != "" {
				t.Fatal("partial result returned")
			}
		})
	}
}
func TestProvisionDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer source.Close()
	client, err := New(source.URL, "private-token", source.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := schemetransfer.ProvisionRequest{Kind: "tag", Action: "create", Key: "tag", Name: "Tag"}
	_, err = client.ProvisionResource(context.Background(), "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001", "t0", input)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ProvisionResource(ctx, "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001", "t0", input); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
