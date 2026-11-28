// Package crmdefinitions describes the integration-owned subject variables required by a catalog version.
package crmdefinitions

import (
	"fmt"
	"regexp"
	"strings"
)

type Definition struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Set is keyed by the stable CRM variable key in the integration's namespace.
// A name is used only on creation. Existing IDs, labels and subject values survive reconciliation.
type Set map[string]Definition

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (s Set) Validate() error {
	if len(s) > 128 {
		return fmt.Errorf("too many CRM variable definitions")
	}
	for key, d := range s {
		if len(key) > 128 || !keyPattern.MatchString(key) || key == "display_name" || key == "description" {
			return fmt.Errorf("invalid CRM variable key")
		}
		if strings.TrimSpace(d.Name) == "" || len(d.Name) > 512 {
			return fmt.Errorf("invalid CRM variable name for %s", key)
		}
		switch d.Type {
		case "string", "number", "boolean", "datetime", "array", "object":
		default:
			return fmt.Errorf("invalid CRM variable type for %s", key)
		}
	}
	return nil
}
