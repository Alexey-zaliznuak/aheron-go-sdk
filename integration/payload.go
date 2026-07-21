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
//
// SchemeID and StepID identify where the context was parked. They are not
// needed for a regular Resolve, but integrations may use SchemeID for
// per-scheme configuration, while StepsClient.Reactivate requires StepID to
// re-enter this step's output after the context has moved on.
type ExecutionContext struct {
	ID        string  `json:"id"`
	Version   int64   `json:"version"`
	InputKey  *string `json:"inputKey,omitempty"`
	SubjectID string  `json:"subjectId,omitempty"`
	ProjectID string  `json:"projectId,omitempty"`
	SchemeID  string  `json:"schemeId,omitempty"`
	StepID    string  `json:"stepId,omitempty"`
}

// InstallRequest is the fixed body the platform POSTs to the integration's
// install_url when the integration is installed into a project. ProjectAPIKey is
// the project API key the integration uses for CRM calls; it is delivered once,
// on install, and the integration must persist it against ProjectID.
type InstallRequest struct {
	ProjectID     string `json:"projectId"`
	ProjectAPIKey string `json:"projectApiKey"`
}

// UninstallRequest is the fixed body the platform POSTs to the integration's
// uninstall_url when the integration is removed from a project. The integration
// should drop its per-project state — most importantly the project API key it
// stored on install — so it no longer acts on the project's behalf.
type UninstallRequest struct {
	ProjectID string `json:"projectId"`
}

// TriggerSyncRequest is the fixed body the platform POSTs to the integration's
// trigger_sync_url after a project's trigger-block configuration changed. It is a
// ping, not the configuration itself: on receipt the integration re-reads the
// current listing with TriggersClient.ListTriggers.
//
// ConfigVersion is a per-(project, integration) counter the platform increments
// transactionally with each configuration change. It lets the integration ignore
// out-of-order or duplicate deliveries: apply a freshly fetched snapshot only
// when its version is greater than the last one already applied, and guard the
// local snapshot by that version.
type TriggerSyncRequest struct {
	ProjectID     string `json:"projectId"`
	BlockKey      string `json:"blockKey"`
	ConfigVersion int64  `json:"configVersion"`
}

// VariableValuesRequest is the fixed body the platform POSTs to an
// integration-provided variable-values endpoint. Query, Cursor and Limit select
// search mode. Values selects resolve mode, in which the integration returns
// labels for the supplied stored values.
type VariableValuesRequest struct {
	ProjectID   string   `json:"projectId"`
	VariableKey string   `json:"variableKey"`
	Query       *string  `json:"query,omitempty"`
	Cursor      *string  `json:"cursor,omitempty"`
	Limit       *int     `json:"limit,omitempty"`
	Values      []string `json:"values,omitempty"`
}

// VariableValueItem is one selectable value returned by an integration.
type VariableValueItem struct {
	Value string  `json:"value"`
	Title string  `json:"title"`
	Icon  *string `json:"icon,omitempty"`
}

// VariableValuesResponse is the response from a variable-values endpoint.
// NextCursor is used only for search pagination.
type VariableValuesResponse struct {
	Items      []VariableValueItem `json:"items"`
	NextCursor *string             `json:"nextCursor,omitempty"`
}
