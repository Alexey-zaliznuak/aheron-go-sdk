// Package variables implements the versioned, IO-free Aheron variable language.
// It separates reference identity from values so execution and template export
// can use the same parser.
package variables

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const DialectV1 = "aheronVarsV1"

// Scope identifies the owner of a reference, independently of installed
// integrations or the presence of a runtime value.
type Scope string

const (
	ScopeSubject     Scope = "subject"
	ScopeProject     Scope = "project"
	ScopeIntegration Scope = "integration"
	ScopeRuntime     Scope = "runtime"
)

var ErrInvalidReference = errors.New("invalid variable reference")

// Reference addresses a definition and an optional path inside its value.
// Namespace is an integration slug for ScopeIntegration, and project, context
// or integrationState for ScopeRuntime. It is empty for subject/project refs.
// Key may be a definition key or ID; resolving that identity belongs to the
// caller's definition catalog, not the syntax parser.
type Reference struct {
	Scope      Scope    `json:"scope"`
	Namespace  string   `json:"namespace,omitempty"`
	Key        string   `json:"key"`
	AccessPath []string `json:"accessPath,omitempty"`
}

// ParseReferenceV1 parses the contents of {{...}}, without braces. Bare keys
// always mean subject variables. Other than reserved namespaces, a dotted
// prefix ALWAYS means an integration slug; it never depends on what is installed.
// Use subject.order.total to access a subject object's field.
func ParseReferenceV1(key string) (Reference, error) {
	parts := strings.Split(strings.TrimSpace(key), ".")
	for _, part := range parts {
		if !validSegment(part) {
			return Reference{}, ErrInvalidReference
		}
	}
	ref := Reference{Scope: ScopeSubject, Key: parts[0]}
	if len(parts) == 1 {
		return ref, nil
	}
	ref.Key = parts[1]
	ref.AccessPath = append([]string(nil), parts[2:]...)
	switch parts[0] {
	case "subject":
	case "project":
		ref.Scope = ScopeProject
		if ref.Key == "id" {
			ref.Scope, ref.Namespace = ScopeRuntime, "project"
		}
	case "context", "integrationState":
		ref.Scope, ref.Namespace = ScopeRuntime, parts[0]
	default:
		ref.Scope, ref.Namespace = ScopeIntegration, parts[0]
	}
	return ref, nil
}

// Expression returns the explicit qualified expression without braces. It
// rejects manually constructed references that would change meaning on reparse.
func (r Reference) Expression() (string, error) {
	prefix := ""
	switch r.Scope {
	case ScopeSubject, ScopeProject:
		if r.Namespace != "" {
			return "", ErrInvalidReference
		}
		prefix = string(r.Scope)
	case ScopeIntegration, ScopeRuntime:
		prefix = r.Namespace
	default:
		return "", ErrInvalidReference
	}
	parts := append([]string{prefix, r.Key}, r.AccessPath...)
	expression := strings.Join(parts, ".")
	parsed, err := ParseReferenceV1(expression)
	if err != nil || parsed.Scope != r.Scope || parsed.Namespace != r.Namespace || parsed.Key != r.Key || !samePath(parsed.AccessPath, r.AccessPath) {
		return "", fmt.Errorf("%w: reference cannot be represented in %s", ErrInvalidReference, DialectV1)
	}
	return expression, nil
}

func validSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		if r != '_' && r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
