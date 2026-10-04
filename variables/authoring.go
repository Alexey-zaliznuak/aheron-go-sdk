package variables

import _ "embed"

//go:embed authoring.md
var authoringGuide string

// AuthoringGuide describes DialectV1 beside its parser. Runtime publishers can
// embed this exact SDK version's guide without copying the language contract.
// Available values, missing-value policy and persistence belong to the caller.
func AuthoringGuide() string { return authoringGuide }
