package integration

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

func TestSubjectTagsContract(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			params := UpdateSubjectTagsParams{AddTags: []string{crmOAuthIntegration}, RemoveTags: []string{crmOAuthProject}}
			want := []SubjectTag{{ProjectID: crmOAuthProject, SubjectID: crmOAuthSubject, TagID: crmOAuthIntegration, CreatedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)}}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.URL.Path != "/projects/"+crmOAuthProject+"/subjects/"+crmOAuthSubject+"/tags" || r.Header.Get("Authorization") != "Bearer project-key" {
					t.Error("wrong subject tags request")
				}
				body, _ := io.ReadAll(r.Body)
				if method == http.MethodPatch {
					if r.Header.Get("Content-Type") != "application/json" || string(body) != `{"addTags":["`+crmOAuthIntegration+`"],"removeTags":["`+crmOAuthProject+`"]}` {
						t.Errorf("wrong delta body: %s", body)
					}
				} else if len(body) != 0 {
					t.Errorf("GET sent body: %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(want)
			}))
			defer srv.Close()
			client, err := New(Config{CRMURL: srv.URL, APIKey: "project-key"})
			if err != nil {
				t.Fatal(err)
			}
			var got []SubjectTag
			if method == http.MethodGet {
				got, err = client.CRM.ListSubjectTags(t.Context(), crmOAuthProject, crmOAuthSubject)
			} else {
				got, err = client.CRM.UpdateSubjectTags(t.Context(), crmOAuthProject, crmOAuthSubject, params)
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("subject tags = %#v, %v; want %#v", got, err, want)
			}
		})
	}
}

func TestSubjectTagsOAuthRejection(t *testing.T) {
	c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached network")
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, ids := range [][2]string{
		{crmOAuthSubject, crmOAuthSubject},
		{crmOAuthProject + "/../" + crmOAuthSubject, crmOAuthSubject},
		{crmOAuthProject, "../tags"},
		{crmOAuthProject, crmOAuthSubject + "/../" + crmOAuthSubject},
		{crmOAuthProject, crmOAuthSubject + "?admin=true"},
		{crmOAuthProject, "00000000-0000-0000-0000-000000000000"},
		{crmOAuthProject, "AAAAAAAA-1111-1111-1111-111111111111"},
	} {
		if _, err := c.ListSubjectTags(t.Context(), ids[0], ids[1]); !errors.Is(err, integrationoauth.ErrRequest) {
			t.Errorf("ListSubjectTags(%q, %q): %v", ids[0], ids[1], err)
		}
		if _, err := c.UpdateSubjectTags(t.Context(), ids[0], ids[1], UpdateSubjectTagsParams{}); !errors.Is(err, integrationoauth.ErrRequest) {
			t.Errorf("UpdateSubjectTags(%q, %q): %v", ids[0], ids[1], err)
		}
	}
	for _, ids := range [][2]string{{"", crmOAuthSubject}, {crmOAuthProject, ""}} {
		if _, err := c.ListSubjectTags(t.Context(), ids[0], ids[1]); err == nil {
			t.Error("ListSubjectTags accepted empty id")
		}
		if _, err := c.UpdateSubjectTags(t.Context(), ids[0], ids[1], UpdateSubjectTagsParams{}); err == nil {
			t.Error("UpdateSubjectTags accepted empty id")
		}
	}
}

func TestSubjectTagsOAuthFailures(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			{"forbidden", "reflected-secret", http.StatusForbidden},
			{"unavailable", "reflected-secret", http.StatusServiceUnavailable},
			{"decode", `[{"createdAt":"reflected-secret"}]`, http.StatusOK},
			{"shape", `{}`, http.StatusOK},
			{"unexpectedStatus", `[]`, http.StatusCreated},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				calls := 0
				c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/oauth/token" {
						crmOAuthIssue(t, w, r, 1)
						return
					}
					calls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				var err error
				if method == http.MethodGet {
					_, err = c.ListSubjectTags(t.Context(), crmOAuthProject, crmOAuthSubject)
				} else {
					_, err = c.UpdateSubjectTags(t.Context(), crmOAuthProject, crmOAuthSubject, UpdateSubjectTagsParams{AddTags: []string{crmOAuthIntegration}})
				}
				if err == nil || strings.Contains(err.Error(), "secret") || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				if tc.status >= 400 && StatusCode(err) != tc.status {
					t.Fatalf("lost error status: %v", err)
				}
			})
		}
	}
}
