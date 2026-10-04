package variables_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/variables"
)

func TestAuthoringExamples(t *testing.T) {
	guide := strings.ReplaceAll(variables.AuthoringGuide(), "\r\n", "\n")
	parts := strings.Split(guide, "```json\n")
	if len(parts) != 2 {
		t.Fatal("expected one executable fixture in language guide")
	}
	var fixture struct {
		Values struct {
			Subject          map[string]any            `json:"subject"`
			Project          map[string]any            `json:"project"`
			Integrations     map[string]map[string]any `json:"integrations"`
			ProjectID        string                    `json:"projectId"`
			Context          map[string]any            `json:"context"`
			IntegrationState map[string]any            `json:"integrationState"`
		} `json:"values"`
		Examples []struct {
			Template      string `json:"template"`
			MissingPolicy string `json:"missingPolicy"`
			Expected      string `json:"expected"`
			Error         string `json:"error"`
		} `json:"examples"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.Split(parts[1], "\n```")[0]))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil || len(fixture.Examples) == 0 {
		t.Fatalf("invalid guide fixture: %v", err)
	}
	v := fixture.Values
	values := variables.Values{Subject: v.Subject, Project: v.Project, Integrations: v.Integrations, ProjectID: v.ProjectID, Context: v.Context, IntegrationState: v.IntegrationState}
	for _, example := range fixture.Examples {
		t.Run(example.Template, func(t *testing.T) {
			var options variables.RenderOptions
			switch example.MissingPolicy {
			case "", "error":
			case "empty":
				options.Missing = variables.MissingEmpty
			default:
				t.Fatalf("unknown missing policy %q", example.MissingPolicy)
			}
			template, err := variables.ParseV1(example.Template)
			var got string
			if err == nil {
				got, err = template.Render(values.Lookup, options)
			}
			switch example.Error {
			case "":
				if err != nil || got != example.Expected {
					t.Fatalf("got %q (%v), want %q", got, err, example.Expected)
				}
			case "syntax":
				if !errors.Is(err, variables.ErrInvalidTemplate) || got != "" {
					t.Fatalf("expected atomic syntax failure, got %q (%v)", got, err)
				}
			case "missing":
				if !errors.Is(err, variables.ErrUnresolved) || got != "" {
					t.Fatalf("expected missing value, got %q (%v)", got, err)
				}
			default:
				t.Fatalf("unknown error %q", example.Error)
			}
		})
	}
}
