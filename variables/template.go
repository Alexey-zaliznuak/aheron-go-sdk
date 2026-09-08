package variables

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidTemplate = errors.New("invalid variable template")
	ErrUnresolved      = errors.New("variable reference not found")
)

// SyntaxError includes a byte offset, never the source text (which may contain
// private settings). A malformed placeholder prevents export of the whole field.
type SyntaxError struct{ Offset int }

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%v at byte %d", ErrInvalidTemplate, e.Offset)
}
func (e *SyntaxError) Unwrap() error { return ErrInvalidTemplate }

// Part is literal text or a reference, with its original half-open byte range.
// Parts() returns deep copies; modifying them does not modify the parsed template.
type Part struct {
	Text      string
	Reference *Reference
	Start     int
	End       int
}

type Template struct{ parts []Part }

// ParseV1 parses a complete text field. Unlike the legacy regex replacement,
// malformed/unfinished {{...}} is an error, not a silently untracked dependency.
// Single braces and unmatched closing braces in literal text are left alone.
func ParseV1(text string) (Template, error) {
	var result Template
	for offset := 0; offset < len(text); {
		start := strings.Index(text[offset:], "{{")
		if start < 0 {
			result.parts = append(result.parts, Part{Text: text[offset:], Start: offset, End: len(text)})
			break
		}
		start += offset
		if start > offset {
			result.parts = append(result.parts, Part{Text: text[offset:start], Start: offset, End: start})
		}
		end := strings.Index(text[start+2:], "}}")
		if end < 0 {
			return Template{}, &SyntaxError{Offset: start}
		}
		end += start + 2
		ref, err := ParseReferenceV1(text[start+2 : end])
		if err != nil {
			return Template{}, &SyntaxError{Offset: start}
		}
		end += 2
		result.parts = append(result.parts, Part{Reference: &ref, Start: start, End: end})
		offset = end
	}
	return result, nil
}

func (t Template) Parts() []Part {
	parts := append([]Part(nil), t.parts...)
	for i, part := range parts {
		if part.Reference != nil {
			ref := *part.Reference
			ref.AccessPath = append([]string(nil), ref.AccessPath...)
			parts[i].Reference = &ref
		}
	}
	return parts
}

// Resolver obtains one value. The caller supplies its own IO/cancellation via a
// closure. found distinguishes a missing definition/path from a present null.
type Resolver func(Reference) (value any, found bool, err error)

type MissingPolicy uint8

const (
	// MissingError is the default: a missing dependency must not silently disappear.
	MissingError MissingPolicy = iota
	// MissingEmpty is for runtime text fields whose contract permits empty values.
	// It must not be used to discover dependencies for template export.
	MissingEmpty
)

type RenderOptions struct {
	Missing MissingPolicy
	// Escape is applied once to each value, never to the surrounding literal text.
	Escape func(string) string
}

// Render formats runtime values. It never evaluates expressions or reparses
// substituted strings. Errors return no partial output and retain their cause.
func (t Template) Render(resolve Resolver, options RenderOptions) (string, error) {
	if options.Missing != MissingError && options.Missing != MissingEmpty {
		return "", fmt.Errorf("variables: unsupported missing policy")
	}
	var out strings.Builder
	for _, part := range t.parts {
		if part.Reference == nil {
			out.WriteString(part.Text)
			continue
		}
		if resolve == nil {
			return "", fmt.Errorf("variables: resolver is required")
		}
		ref := *part.Reference
		ref.AccessPath = append([]string(nil), ref.AccessPath...)
		value, found, err := resolve(ref)
		if err != nil {
			return "", fmt.Errorf("resolve variable at byte %d: %w", part.Start, err)
		}
		if !found {
			if options.Missing == MissingEmpty {
				continue
			}
			return "", fmt.Errorf("%w at byte %d", ErrUnresolved, part.Start)
		}
		text, err := Format(value)
		if err != nil {
			return "", fmt.Errorf("format variable at byte %d: %w", part.Start, err)
		}
		if options.Escape != nil {
			text = options.Escape(text)
		}
		out.WriteString(text)
	}
	return out.String(), nil
}

// Rewrite rewrites references rather than runtime values. A scheme exporter can
// use Parts() to bind definitions to resource refs; an importer can use Rewrite
// to restore qualified target expressions. It does not resolve legacy semantics.
func (t Template) Rewrite(mapReference func(Reference) (Reference, error)) (string, error) {
	var out strings.Builder
	for _, part := range t.parts {
		if part.Reference == nil {
			out.WriteString(part.Text)
			continue
		}
		if mapReference == nil {
			return "", fmt.Errorf("variables: reference mapper is required")
		}
		ref := *part.Reference
		ref.AccessPath = append([]string(nil), ref.AccessPath...)
		mapped, err := mapReference(ref)
		if err != nil {
			return "", fmt.Errorf("map variable at byte %d: %w", part.Start, err)
		}
		expression, err := mapped.Expression()
		if err != nil {
			return "", fmt.Errorf("map variable at byte %d: %w", part.Start, err)
		}
		out.WriteString("{{")
		out.WriteString(expression)
		out.WriteString("}}")
	}
	return out.String(), nil
}
