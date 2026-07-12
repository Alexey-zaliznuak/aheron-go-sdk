package integration

// This file defines the typed values the platform embeds in the requests it
// sends to an integration backend. Unlike earlier versions of the SDK, the
// action request body is NOT a fixed envelope: the integration author designs it
// per version through the platform's action_request_template, referencing the
// placeholders {{context}}, {{actionKey}}, {{blockSettings}}, {{vars}} and
// {{integrationContext}}. The integration therefore decodes the body into its
// own struct with DecodeBody, embedding ExecutionContext wherever it templated
// {{context}}. The install request, by contrast, has a fixed shape.

// ExecutionContext identifies a parked integrationAction step. The platform
// substitutes it wherever the author's action_request_template references
// {{context}}. The integration passes it straight to StepsClient.Resolve to
// advance the step; correlation is by (ID, Version). InputKey is the input port
// the subject entered the block through (nil when the block has no inputs).
//
// SubjectID and ProjectID identify the subject (CRM lead) and project the step
// runs for, letting the integration resolve its own per-subject state (e.g. map
// the subject to an external messenger user it already stores). They are omitted
// from the resolve call, which correlates only by (ID, Version).
type ExecutionContext struct {
	ID        string  `json:"id"`
	Version   int64   `json:"version"`
	InputKey  *string `json:"inputKey,omitempty"`
	SubjectID string  `json:"subjectId,omitempty"`
	ProjectID string  `json:"projectId,omitempty"`
}

// InstallRequest is the fixed body the platform POSTs to the integration's
// install_url when the integration is installed into a project. ProjectAPIKey is
// the project API key the integration uses for CRM calls; it is delivered once,
// on install, and the integration must persist it against ProjectID.
type InstallRequest struct {
	ProjectID     string `json:"projectId"`
	ProjectAPIKey string `json:"projectApiKey"`
}
