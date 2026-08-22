package integration

// This file defines the catalog manifest: the integration's own declaration of
// what it offers the platform. It is the counterpart of the catalog rows the
// platform stores — the version's HTTP contract (install/action/trigger-sync/
// variable-values endpoints) and the block declarations inside it.
//
// The point of declaring it in code is that there is then one source of truth.
// Before this existed the block set was typed into the platform UI by hand and
// the integration's routes had to be kept in step with it manually, so a
// renamed output or a new block drifted silently until a scheme broke.
//
// Endpoints are declared as PATHS, not URLs: the deployment address belongs to
// the environment (Config.PublicBaseURL), not to the source. An absolute http(s)
// path is still accepted for the rare endpoint that lives on another host.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Kind is the block kind the platform renders and executes.
type Kind string

// The block kinds the platform supports. They match the values accepted by the
// catalog: a step of the corresponding type is created when the block is placed
// on a scheme.
const (
	// KindAction is a block the engine runs and then parks, waiting for the
	// integration to resolve it (StepsClient.Resolve).
	KindAction Kind = "action"
	// KindTrigger is a block that waits for the integration to activate it
	// (TriggersClient.Activate) and routes by activation key.
	KindTrigger Kind = "trigger"
	// KindSubflow is a block whose body is a sub-scheme authored in the platform
	// UI on the integration's draft version.
	KindSubflow Kind = "subflow"
)

// VariableValueSource declares that the values of one variable are supplied by
// the integration rather than typed by the user. Type is currently always
// "remote"; the struct exists so a source can grow options without a wire break.
type VariableValueSource struct {
	Type string `json:"type"`
}

// RemoteVariableValues is the only variable value source the platform supports:
// the block editor asks the integration's variable-values endpoint for the
// selectable values.
func RemoteVariableValues() VariableValueSource {
	return VariableValueSource{Type: "remote"}
}

// Block is one block declaration. Key is the stable identity — it is what a
// scheme step stores, so renaming it is removing one block and adding another
// (see Manifest.Retired).
//
// Outputs are the block's output ports, in the order they should be rendered.
// Inputs are the input ports of a KindSubflow host block and are empty for the
// other kinds.
type Block struct {
	// Key is the block's slug, matching ^[a-zA-Z0-9_-]+$ (e.g. "create-row").
	Key string
	// Kind selects how the platform executes and renders the block.
	Kind Kind
	// Name is the human-readable label shown in the scheme editor.
	Name string
	// Description is optional help text for the block palette.
	Description string
	// Color optionally overrides the editor colour, as #RRGGBB. Empty uses the
	// default colour of the kind.
	Color string
	// IframePath is the settings editor page of the block. Empty defaults to
	// "/blocks/<Key>", which is the convention every integration follows, so
	// most blocks leave it unset.
	IframePath string
	// Inputs are the declared input ports (KindSubflow only).
	Inputs []string
	// Outputs are the declared output ports.
	Outputs []string
	// SubflowDeclaration carries optional extra metadata for a KindSubflow
	// block. It is passed to the platform verbatim.
	SubflowDeclaration json.RawMessage
}

// ConsolePage is one page of the integration's console that the platform pins
// into the sidebar of every project the integration is installed into.
//
// It exists to shorten a daily path: without it, reaching a section like the
// dialogues of a messenger integration means opening the project, the
// integrations list, the integration, its console and finally the tab.
//
// The page still opens as the console does — inside the platform, in an iframe,
// authorized by a short-lived console view-token — so it must be a page the
// integration already serves under its console.
type ConsolePage struct {
	// Key is the page's slug, matching ^[a-zA-Z0-9_-]+$ (e.g. "dialogs"). It
	// identifies the entry, so changing it replaces one menu item with another.
	Key string
	// Label is the text shown next to the icon in the sidebar.
	Label string
	// Path is the page itself (e.g. "/console/dialogs").
	Path string
	// Icon is optional. Without one the platform renders the entry with its own
	// fallback, so an integration can declare the page now and add artwork later.
	Icon Icon
}

// Icon is a small image the platform stores in its own bucket and serves from
// there. It travels inside the manifest rather than as a URL on the integration's
// host: the sidebar of a project must not break when the integration is down,
// and the platform will not fetch a URL an integration controls.
type Icon struct {
	// MimeType must be one of image/svg+xml, image/png or image/webp.
	MimeType string
	// Content is the raw image bytes, at most 32 KiB. They are sent as base64.
	Content []byte
}

// Manifest is the full desired state of the integration's catalog entry: the
// version contract plus the block set. It is declarative — whatever it omits is
// cleared in the catalog, and whatever it declares replaces what is there.
//
// A zero path means "this integration does not offer that endpoint".
type Manifest struct {
	// ConsolePath is the integration's console page, opened inside a project.
	ConsolePath string
	// ConsolePages are the console pages the platform shows in a project's
	// sidebar. Declaring none leaves the integration reachable the long way,
	// through the integrations list.
	ConsolePages []ConsolePage
	// InstallPath receives InstallRequest when the integration is installed
	// into a project, UninstallPath receives UninstallRequest when it is
	// removed.
	InstallPath   string
	UninstallPath string
	// ActionPath is the single endpoint every action block of this version is
	// called on; ActionRequestTemplate is the body template the platform fills
	// in (placeholders {{context}}, {{actionKey}}, {{blockSettings}}, {{vars}},
	// {{integrationContext}}).
	ActionPath            string
	ActionRequestTemplate json.RawMessage
	// TriggerSyncPath receives TriggerSyncRequest after a project's trigger
	// configuration changed.
	TriggerSyncPath string
	// VariableValuesPath serves VariableValuesRequest for the variables listed
	// in VariableValueSources. Declaring sources without a path is rejected.
	VariableValuesPath   string
	VariableValueSources map[string]VariableValueSource
	// Blocks is the complete block set of the version.
	Blocks []Block
	// Retired lists block keys that used to be declared and are deliberately
	// gone. The platform refuses to drop a published block that is not listed
	// here: a block key silently disappearing from the catalog breaks the
	// schemes that already use it, and an accidental deletion in code (a
	// mis-merge, a build tag) looks exactly like an intentional one on the
	// wire. Listing the key is the author saying "yes, I mean it".
	Retired []string
}

// manifestBody is the wire shape of a resolved manifest. Paths have become
// absolute URLs; an endpoint the manifest does not offer is omitted, which the
// platform reads as "clear it".
type manifestBody struct {
	ConsoleURL            string                         `json:"consoleUrl,omitempty"`
	ConsolePages          []consolePageBody              `json:"consolePages,omitempty"`
	InstallURL            string                         `json:"installUrl,omitempty"`
	UninstallURL          string                         `json:"uninstallUrl,omitempty"`
	ActionURL             string                         `json:"actionUrl,omitempty"`
	ActionRequestTemplate json.RawMessage                `json:"actionRequestTemplate,omitempty"`
	TriggerSyncURL        string                         `json:"triggerSyncUrl,omitempty"`
	VariableValuesURL     string                         `json:"variableValuesUrl,omitempty"`
	VariableValueSources  map[string]VariableValueSource `json:"variableValueSources,omitempty"`
	Blocks                []blockBody                    `json:"blocks"`
	Retired               []string                       `json:"retired,omitempty"`
}

// consolePageBody is the wire shape of one resolved console page. Icon bytes
// marshal as base64, which is what []byte does in encoding/json.
type consolePageBody struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	URL   string    `json:"url"`
	Icon  *iconBody `json:"icon,omitempty"`
}

type iconBody struct {
	MimeType string `json:"mimeType"`
	Content  []byte `json:"content"`
}

// blockBody is the wire shape of one resolved block declaration. Field names
// match the platform's block declaration payload.
type blockBody struct {
	BlockKey           string          `json:"blockKey"`
	Kind               string          `json:"kind"`
	Name               string          `json:"name"`
	Description        string          `json:"description,omitempty"`
	Color              string          `json:"color,omitempty"`
	IframeURL          string          `json:"iframeUrl,omitempty"`
	Inputs             []string        `json:"inputs"`
	Outputs            []string        `json:"outputs"`
	SubflowDeclaration json.RawMessage `json:"subflowDeclaration,omitempty"`
}

// validKinds is the set accepted by the platform, checked here so a typo fails
// in the integration rather than as an opaque 400 from the catalog.
var validKinds = map[Kind]bool{KindAction: true, KindTrigger: true, KindSubflow: true}

// resolve validates the manifest and turns its paths into absolute URLs against
// baseURL. It reports the first problem it finds; every check is cheap and
// local, so a broken manifest is caught before the request is signed.
func (m Manifest) resolve(baseURL string) (manifestBody, error) {
	base := strings.TrimRight(baseURL, "/")

	consoleURL, err := resolveURL(base, m.ConsolePath, "ConsolePath")
	if err != nil {
		return manifestBody{}, err
	}
	installURL, err := resolveURL(base, m.InstallPath, "InstallPath")
	if err != nil {
		return manifestBody{}, err
	}
	uninstallURL, err := resolveURL(base, m.UninstallPath, "UninstallPath")
	if err != nil {
		return manifestBody{}, err
	}
	actionURL, err := resolveURL(base, m.ActionPath, "ActionPath")
	if err != nil {
		return manifestBody{}, err
	}
	triggerSyncURL, err := resolveURL(base, m.TriggerSyncPath, "TriggerSyncPath")
	if err != nil {
		return manifestBody{}, err
	}
	variableValuesURL, err := resolveURL(base, m.VariableValuesPath, "VariableValuesPath")
	if err != nil {
		return manifestBody{}, err
	}

	if len(m.VariableValueSources) > 0 && variableValuesURL == "" {
		return manifestBody{}, fmt.Errorf("integration: manifest declares VariableValueSources without VariableValuesPath")
	}
	if len(m.ActionRequestTemplate) > 0 && !json.Valid(m.ActionRequestTemplate) {
		return manifestBody{}, fmt.Errorf("integration: manifest ActionRequestTemplate is not valid JSON")
	}

	pages, err := m.resolveConsolePages(base)
	if err != nil {
		return manifestBody{}, err
	}

	retired := make(map[string]bool, len(m.Retired))
	for _, key := range m.Retired {
		if key == "" {
			return manifestBody{}, fmt.Errorf("integration: manifest Retired contains an empty block key")
		}
		retired[key] = true
	}

	blocks := make([]blockBody, 0, len(m.Blocks))
	seen := make(map[string]bool, len(m.Blocks))
	for i, b := range m.Blocks {
		if b.Key == "" {
			return manifestBody{}, fmt.Errorf("integration: manifest block #%d has no Key", i)
		}
		if seen[b.Key] {
			return manifestBody{}, fmt.Errorf("integration: manifest declares block %q twice", b.Key)
		}
		seen[b.Key] = true
		if retired[b.Key] {
			return manifestBody{}, fmt.Errorf("integration: manifest block %q is both declared and Retired", b.Key)
		}
		if !validKinds[b.Kind] {
			return manifestBody{}, fmt.Errorf("integration: manifest block %q has invalid Kind %q", b.Key, b.Kind)
		}
		if b.Name == "" {
			return manifestBody{}, fmt.Errorf("integration: manifest block %q has no Name", b.Key)
		}

		iframePath := b.IframePath
		if iframePath == "" {
			iframePath = "/blocks/" + b.Key
		}
		iframeURL, err := resolveURL(base, iframePath, fmt.Sprintf("block %q IframePath", b.Key))
		if err != nil {
			return manifestBody{}, err
		}

		blocks = append(blocks, blockBody{
			BlockKey:           b.Key,
			Kind:               string(b.Kind),
			Name:               b.Name,
			Description:        b.Description,
			Color:              b.Color,
			IframeURL:          iframeURL,
			Inputs:             nonNil(b.Inputs),
			Outputs:            nonNil(b.Outputs),
			SubflowDeclaration: b.SubflowDeclaration,
		})
	}

	return manifestBody{
		ConsoleURL:            consoleURL,
		ConsolePages:          pages,
		InstallURL:            installURL,
		UninstallURL:          uninstallURL,
		ActionURL:             actionURL,
		ActionRequestTemplate: m.ActionRequestTemplate,
		TriggerSyncURL:        triggerSyncURL,
		VariableValuesURL:     variableValuesURL,
		VariableValueSources:  m.VariableValueSources,
		Blocks:                blocks,
		Retired:               m.Retired,
	}, nil
}

// Limits on the sidebar declaration. The whole manifest, icons included, travels
// in one signed request, and the platform caps that body at 1 MiB — these two
// numbers are what keeps a manifest comfortably inside it. They are also a sanity
// bound on the sidebar itself: a menu of eight entries is already a lot.
const (
	maxConsolePages = 8
	maxIconBytes    = 32 * 1024
)

// iconMimeTypes is the set the platform stores. It is short because the browser
// renders these straight from the platform's bucket.
var iconMimeTypes = map[string]bool{
	"image/svg+xml": true,
	"image/png":     true,
	"image/webp":    true,
}

// resolveConsolePages validates the sidebar declaration and turns page paths into
// absolute URLs.
func (m Manifest) resolveConsolePages(base string) ([]consolePageBody, error) {
	if len(m.ConsolePages) == 0 {
		return nil, nil
	}
	if len(m.ConsolePages) > maxConsolePages {
		return nil, fmt.Errorf("integration: manifest declares %d console pages, at most %d are allowed", len(m.ConsolePages), maxConsolePages)
	}

	pages := make([]consolePageBody, 0, len(m.ConsolePages))
	seen := make(map[string]bool, len(m.ConsolePages))
	for i, p := range m.ConsolePages {
		if p.Key == "" {
			return nil, fmt.Errorf("integration: manifest console page #%d has no Key", i)
		}
		if seen[p.Key] {
			return nil, fmt.Errorf("integration: manifest declares console page %q twice", p.Key)
		}
		seen[p.Key] = true
		if p.Label == "" {
			return nil, fmt.Errorf("integration: manifest console page %q has no Label", p.Key)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("integration: manifest console page %q has no Path", p.Key)
		}
		url, err := resolveURL(base, p.Path, fmt.Sprintf("console page %q Path", p.Key))
		if err != nil {
			return nil, err
		}

		page := consolePageBody{Key: p.Key, Label: p.Label, URL: url}
		if len(p.Icon.Content) > 0 || p.Icon.MimeType != "" {
			icon, err := p.Icon.validate(p.Key)
			if err != nil {
				return nil, err
			}
			page.Icon = &icon
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func (i Icon) validate(pageKey string) (iconBody, error) {
	if !iconMimeTypes[i.MimeType] {
		return iconBody{}, fmt.Errorf("integration: console page %q icon has unsupported MimeType %q", pageKey, i.MimeType)
	}
	if len(i.Content) == 0 {
		return iconBody{}, fmt.Errorf("integration: console page %q icon has no Content", pageKey)
	}
	if len(i.Content) > maxIconBytes {
		return iconBody{}, fmt.Errorf("integration: console page %q icon is %d bytes, at most %d are allowed", pageKey, len(i.Content), maxIconBytes)
	}
	return iconBody{MimeType: i.MimeType, Content: i.Content}, nil
}

// IconFromFS reads an icon out of a filesystem — in practice the go:embed FS an
// integration already uses for its web assets, so the icon is part of the binary
// and cannot go missing at runtime. The MIME type comes from the file extension.
func IconFromFS(fsys fs.FS, name string) (Icon, error) {
	content, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Icon{}, fmt.Errorf("integration: read icon %q: %w", name, err)
	}
	mimeType, ok := iconMimeByExt[strings.ToLower(path.Ext(name))]
	if !ok {
		return Icon{}, fmt.Errorf("integration: icon %q has an unsupported extension, want .svg, .png or .webp", name)
	}
	return Icon{MimeType: mimeType, Content: content}, nil
}

var iconMimeByExt = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".webp": "image/webp",
}

// resolveURL joins a manifest path onto the public base URL. An empty path means
// the endpoint is not offered and yields an empty URL. A path that is already
// absolute is taken as given, which is what an integration whose webhook lives
// on another host needs. Anything else must be rooted, so a forgotten leading
// slash cannot quietly produce "https://hostblocks/create-row".
func resolveURL(base, path, field string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("integration: manifest %s %q must start with %q or be an absolute http(s) URL", field, path, "/")
	}
	if base == "" {
		return "", fmt.Errorf("integration: manifest %s %q is relative but no PublicBaseURL is configured", field, path)
	}
	return base + path, nil
}

// nonNil replaces a nil slice with an empty one so it marshals as [] instead of
// null: the platform stores these as JSON documents and an explicit empty list
// is what "no ports declared" means there.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// BlockKeys returns the declared block keys in declaration order. It is what an
// integration uses to route its own iframe pages from the same list the catalog
// is built from, so the two cannot drift.
func (m Manifest) BlockKeys() []string {
	keys := make([]string, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		keys = append(keys, b.Key)
	}
	return keys
}
