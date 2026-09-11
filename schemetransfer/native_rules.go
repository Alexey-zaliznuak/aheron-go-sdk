package schemetransfer

// NativeCopyRules describes the platform's built-in block settings. It is not an
// integration manifest or callback protocol. Integration authors use CopyRules
// and prepare concrete references with CopyBuilder.
type NativeCopyRules struct {
	Version           int                `json:"version"`
	Mode              string             `json:"mode"`
	Settings          *Rule              `json:"settings,omitempty"`
	ImplicitResources []ImplicitResource `json:"implicitResources,omitempty"`
	Validation        *Validation        `json:"validation,omitempty"`
	Reason            string             `json:"reason,omitempty"`
	Notes             string             `json:"notes,omitempty"`
}

type Validation struct {
	Mode    string `json:"mode"`
	Message string `json:"message,omitempty"`
}
