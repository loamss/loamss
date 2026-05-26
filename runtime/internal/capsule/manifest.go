// Package capsule implements the capsule host primitives — manifest
// parsing/validation, capsule lifecycle (install / uninstall),
// subprocess host + MCP-over-stdio. The wire contract is defined
// in capsule-spec.md; this package translates it into Go.
//
// v0.1 components (this file): manifest types + Parse + Validate.
// Capsule installation + subprocess lifecycle land in subsequent
// commits.
package capsule

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SupportedSpecVersions enumerates capsule.spec_version values this
// runtime accepts. Capsules at older versions may still load via a
// future compatibility shim; capsules at newer versions are rejected
// (we can't know what they require).
var SupportedSpecVersions = map[string]bool{
	"0.1": true,
}

// Manifest is the Go-typed view of capsule.yaml. Field tags mirror
// the spec exactly; YAML keys are snake_case throughout. Optional
// fields use pointer/slice/map zero values so a missing key is
// distinguishable from an explicit empty value where the spec cares
// about that distinction.
//
// The runtime treats Manifest as immutable after Parse + Validate.
// Mutating fields after validation invites the manifest to drift
// from what the runtime authorized.
type Manifest struct {
	// SpecVersion is the capsule-spec.md version this manifest
	// conforms to. Must be one of SupportedSpecVersions.
	SpecVersion string `yaml:"spec_version" json:"spec_version"`

	// Name is the capsule identifier. Lowercase letters, digits,
	// and hyphens; must start with a letter. Globally unique within
	// the registry (registry-level enforcement, not runtime-level).
	Name string `yaml:"name" json:"name"`

	// Version is the capsule's semver. Pinned by users at install
	// time; updates within a major version are auto-applicable
	// when the user opts in.
	Version string `yaml:"version" json:"version"`

	// Author identifies who published the capsule. Used by the
	// registry and surfaced on the permission slip.
	Author Author `yaml:"author" json:"author"`

	// Permissions is the list of capabilities the capsule declares
	// it will exercise. Every runtime access must correspond to a
	// declared entry; undeclared accesses are rejected at Check
	// time, not just at install.
	Permissions []PermissionRequest `yaml:"permissions" json:"permissions"`

	// Tools is the set of MCP tools the capsule exposes. Mounted
	// into the runtime's tool registry on capsule install.
	Tools []ToolDef `yaml:"tools" json:"tools"`

	// ModelRequirements describes what kind of model the capsule
	// needs. The router picks the actual provider/model — the
	// capsule doesn't get to choose.
	ModelRequirements ModelRequirements `yaml:"model_requirements" json:"model_requirements"`

	// Runtime captures how the runtime should host this capsule:
	// subprocess type, entrypoint, resource limits.
	Runtime RuntimeSpec `yaml:"runtime" json:"runtime"`

	// MemoryExtensions, if present, declares new entity types this
	// capsule wants registered. See capsule-spec.md §Memory
	// extensions for namespacing rules.
	MemoryExtensions *MemoryExtensions `yaml:"memory_extensions,omitempty" json:"memory_extensions,omitempty"`

	// Roles enumerates the capsule taxonomy roles this capsule plays.
	// Allowed values: ingestor, organizer, exposer, actuator (a capsule
	// may wear multiple roles). When "ingestor" is present, the
	// Ingestor block must be set. See capsule-spec.md §Roles +
	// extensibility.md §"the capsule taxonomy roles".
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`

	// Ingestor configures the capsule as a data-source connector
	// (see docs/capsule-ingestor-primitives.md). Required when "ingestor"
	// appears in Roles; ignored otherwise.
	Ingestor *IngestorSpec `yaml:"ingestor,omitempty" json:"ingestor,omitempty"`

	// OAuth declares an OAuth 2.0 authorization flow the runtime
	// drives on the capsule's behalf — the runtime owns the loopback
	// listener, does the token exchange, and forwards the result to
	// the capsule via the loamss.oauth.completed callback. Optional;
	// any capsule role can use it (ingestors typically do).
	OAuth *OAuthSpec `yaml:"oauth,omitempty" json:"oauth,omitempty"`

	// Optional metadata fields. Surfaced in the registry; not
	// inspected by the runtime.
	Homepage    string   `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Repository  string   `yaml:"repository,omitempty" json:"repository,omitempty"`
	License     string   `yaml:"license,omitempty" json:"license,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Author identifies the capsule's publisher.
type Author struct {
	Name  string `yaml:"name" json:"name"`
	URL   string `yaml:"url,omitempty" json:"url,omitempty"`
	KeyID string `yaml:"key_id,omitempty" json:"key_id,omitempty"`
}

// PermissionRequest is one entry in the manifest's permissions list.
// Mirrors the permission framework's Grant shape minus the issued/
// expires/revoked fields the runtime owns. Scope is opaque JSON
// validated against the capability's declared schema at install time.
type PermissionRequest struct {
	Capability string         `yaml:"capability" json:"capability"`
	Scope      map[string]any `yaml:"scope,omitempty" json:"scope,omitempty"`
	Rationale  string         `yaml:"rationale,omitempty" json:"rationale,omitempty"`

	// RequiresUserApproval, if true, makes every invocation of this
	// capability interactive — the runtime pauses for user confirm
	// before proceeding. Defaults to false; the capability's own
	// DefaultApproval flag may still force it true at the registry
	// level.
	RequiresUserApproval bool `yaml:"requires_user_approval,omitempty" json:"requires_user_approval,omitempty"`
}

// ToolDef declares one MCP tool. Mirrors the upstream MCP Tool
// shape; InputSchema is a JSON Schema object the runtime exposes
// to clients via tools/list. Validation at install time is a
// shape check (must be JSON, must have a "type" field); full
// JSON-Schema validation runs at invocation time.
type ToolDef struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	InputSchema map[string]any `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
}

// ModelRequirements describes the capsule's model needs. The router
// matches these against installed adapters; capsules that hardcode
// a specific provider are non-compliant per capsule-spec.md.
type ModelRequirements struct {
	// Capabilities is the set of capability tags the capsule needs
	// at least one model to provide. "text", "long_context",
	// "embeddings", "vision", "tool_use", etc.
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`

	// MinContextTokens is the minimum context length required.
	// Zero means unconstrained.
	MinContextTokens int `yaml:"min_context_tokens,omitempty" json:"min_context_tokens,omitempty"`

	// PreferredQuality is the routing hint: high | balanced | fast.
	// Treated by the router as a preference, not a hard constraint.
	PreferredQuality string `yaml:"preferred_quality,omitempty" json:"preferred_quality,omitempty"`

	// ForbiddenDataClasses lists data classes the router must not
	// send to hosted models for this capsule. Health-data capsules
	// declare ["health"] to force local model routing.
	ForbiddenDataClasses []string `yaml:"forbidden_data_classes,omitempty" json:"forbidden_data_classes,omitempty"`
}

// RuntimeSpec is the manifest's runtime: section. Tells the host
// how to invoke the capsule subprocess (or wasm module, when that
// lands).
type RuntimeSpec struct {
	// Type is the execution model. v0.1 supports only "subprocess";
	// "wasm" is reserved and rejected at validation time.
	Type string `yaml:"type" json:"type"`

	// Entrypoint is the command to spawn the capsule. The first
	// element is the executable; remaining elements are arguments.
	// Resolved relative to the capsule directory at runtime.
	Entrypoint []string `yaml:"entrypoint" json:"entrypoint"`

	// Protocol is the wire protocol the capsule speaks. v0.1
	// supports only "mcp" (JSON-RPC 2.0 over stdio).
	Protocol string `yaml:"protocol" json:"protocol"`

	// Resources is the soft resource budget. Enforced at runtime
	// via OS-level controls when the host platform supports them;
	// otherwise advisory.
	Resources Resources `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// Resources is the runtime resource budget for a capsule.
type Resources struct {
	MemoryMB int     `yaml:"memory_mb,omitempty" json:"memory_mb,omitempty"`
	CPUQuota float64 `yaml:"cpu_quota,omitempty" json:"cpu_quota,omitempty"`
}

// MemoryExtensions declares custom entity types the capsule wants
// registered with the memory layer. See capsule-spec.md §Memory
// extensions for the full semantics and namespacing rules.
type MemoryExtensions struct {
	EntityTypes []EntityType `yaml:"entity_types" json:"entity_types"`
}

// EntityType is one capsule-declared memory entity type. Name +
// namespace must be reverse-DNS prefixed; the runtime treats
// `namespace/name` as the canonical identifier.
type EntityType struct {
	Name        string         `yaml:"name" json:"name"`
	Namespace   string         `yaml:"namespace" json:"namespace"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Schema      map[string]any `yaml:"schema" json:"schema"`

	// ProvenanceRequired forces every write of this type to carry a
	// source attribution. Most capsule-defined types set this true.
	ProvenanceRequired bool `yaml:"provenance_required,omitempty" json:"provenance_required,omitempty"`

	// DataClasses inherits onto every entry of this type. A read
	// grant that excludes the class will not see these entries.
	DataClasses []string `yaml:"data_classes,omitempty" json:"data_classes,omitempty"`

	// Embedding is the optional pre-write embedding policy. When
	// set, the runtime embeds the listed source fields via the
	// model router (capsule must hold model.call) before storing.
	Embedding *EmbeddingSpec `yaml:"embedding,omitempty" json:"embedding,omitempty"`
}

// EmbeddingSpec is the per-entity-type embedding policy.
type EmbeddingSpec struct {
	SourceFields []string `yaml:"source_fields" json:"source_fields"`
	ModelTask    string   `yaml:"model_task,omitempty" json:"model_task,omitempty"`
}

// IngestorSpec configures an ingestor-role capsule. See the design
// in docs/capsule-ingestor-primitives.md.
type IngestorSpec struct {
	// SourceID is the canonical adapter id under which this capsule
	// registers as a source — e.g. "source:calendar-ingestor".
	// `loamss source list` shows the capsule alongside in-tree
	// connectors using this id.
	SourceID string `yaml:"source_id" json:"source_id"`

	// Schedule sets the cadence the runtime drives the capsule's
	// sync callback on. Required for ingestor capsules — there's no
	// useful "pull data" model without one.
	Schedule IngestorSchedule `yaml:"schedule" json:"schedule"`

	// OnTrigger is the name of the capsule-side tool the runtime
	// invokes at each tick. Must reference one of the names in
	// Tools[]. Typically "sync".
	OnTrigger string `yaml:"on_trigger" json:"on_trigger"`
}

// IngestorSchedule is the manifest-declared cadence for an
// ingestor's sync callback. Values use Go's time.ParseDuration
// short form (e.g. "5m", "15m", "1h").
type IngestorSchedule struct {
	// Interval is the cadence between scheduled syncs. Min 1m, max
	// 24h — anything faster overwhelms typical provider APIs;
	// anything slower means the user gets stale data and should
	// just sync on demand.
	Interval string `yaml:"interval" json:"interval"`

	// Initial, when set, schedules the first sync this long after
	// install. Use for UX (user sees data quickly without burning
	// a full Interval of API quota). Defaults to Interval when
	// absent.
	Initial string `yaml:"initial,omitempty" json:"initial,omitempty"`
}

// OAuthSpec declares the OAuth 2.0 flow a capsule needs. The
// runtime owns the entire flow: it opens the browser, holds the
// PKCE verifier, exchanges the code for tokens, persists them
// under the capsule's credential blob, and refreshes them on
// demand. The capsule's only OAuth surface is `oauth.access_token`.
//
// See docs/capsule-ingestor-primitives.md §4 "OAuth callback
// bridge (revised)" for the design.
type OAuthSpec struct {
	// Provider is the well-known provider identifier ("google",
	// "github", …) the runtime uses to look up endpoints. For
	// providers not in the well-known registry, declare endpoints
	// inline via AuthorizationEndpoint + TokenEndpoint.
	Provider string `yaml:"provider" json:"provider"`

	// AuthorizationEndpoint is required only when Provider is
	// NOT in the runtime's well-known registry. Must be https://.
	AuthorizationEndpoint string `yaml:"authorization_endpoint,omitempty" json:"authorization_endpoint,omitempty"`

	// TokenEndpoint is required only when Provider is NOT in the
	// runtime's well-known registry. Must be https://.
	TokenEndpoint string `yaml:"token_endpoint,omitempty" json:"token_endpoint,omitempty"`

	// Scopes is the list of OAuth scopes to request. At least one
	// must be declared so the user can see what's being asked for
	// at install time.
	Scopes []string `yaml:"scopes" json:"scopes"`

	// ExtraParams are provider-specific query parameters the
	// runtime adds to the authorization URL on top of the
	// provider's defaults. Common Google extras (access_type,
	// prompt) are in the well-known registry; capsules can add
	// per-provider extras here.
	ExtraParams map[string]string `yaml:"extra_params,omitempty" json:"extra_params,omitempty"`
}

// WellKnownOAuthProvider reports whether the given provider name
// is in the runtime's built-in registry. Exposed so the manifest
// validator can decide whether inline endpoints are required.
//
// Implemented as a package-level var so the test suite can swap in
// a fake (the capsule package doesn't import internal/oauth to
// avoid pulling its dependency surface into manifest validation).
var WellKnownOAuthProvider = func(_ string) bool { return false }

// --- parsing -----------------------------------------------------------

// Parse decodes a YAML manifest into a Manifest struct. Returns a
// shape error if the YAML doesn't parse or doesn't match the
// expected structure. Use Validate to check semantic correctness.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // reject unknown YAML keys
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("capsule: parsing manifest: %w", err)
	}
	return &m, nil
}

// --- validation --------------------------------------------------------

// CapabilityRegistry is the minimum surface Validate needs to check
// that declared permissions reference real capabilities. The
// permission package's Store satisfies this interface naturally;
// tests can pass a fake.
//
// Reserved-namespace checks are a pure naming rule and live in this
// package; they don't need a runtime to run. CapabilityRegistry is
// only consulted for "does the runtime actually know about this
// capability?" — the question that requires a live system.
type CapabilityRegistry interface {
	// HasCapability returns whether a capability is registered with
	// the runtime. Implementations should be safe for concurrent use.
	HasCapability(name string) bool
}

// Validate checks the manifest's semantic correctness. Shape errors
// (missing fields, wrong types) are returned eagerly via the first
// failed check. The optional registry is consulted for capability
// existence; pass nil to skip the capability-registry checks (useful
// for offline validation — `loamss capsule validate` against a
// runtime that hasn't been initialized).
//
// Returns a multi-error grouping every failed check, not just the
// first. Authors fixing manifests get a complete punch list rather
// than one round-trip per problem.
func (m *Manifest) Validate(reg CapabilityRegistry) error {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// --- spec_version ----------------------------------------------
	if m.SpecVersion == "" {
		collect(errors.New("spec_version is required"))
	} else if !SupportedSpecVersions[m.SpecVersion] {
		supported := make([]string, 0, len(SupportedSpecVersions))
		for k := range SupportedSpecVersions {
			supported = append(supported, k)
		}
		collect(fmt.Errorf("spec_version %q not supported (runtime accepts %v)", m.SpecVersion, supported))
	}

	// --- name + version --------------------------------------------
	if m.Name == "" {
		collect(errors.New("name is required"))
	} else if !nameRegex.MatchString(m.Name) {
		collect(fmt.Errorf("name %q invalid (lowercase letters/digits/hyphens; must start with a letter)", m.Name))
	}
	if m.Version == "" {
		collect(errors.New("version is required"))
	} else if !semverRegex.MatchString(m.Version) {
		collect(fmt.Errorf("version %q is not semver (e.g., 1.2.3 or 1.2.3-beta.1)", m.Version))
	}

	// --- author ----------------------------------------------------
	if m.Author.Name == "" {
		collect(errors.New("author.name is required"))
	}

	// --- permissions ----------------------------------------------
	if len(m.Permissions) == 0 {
		collect(errors.New("permissions: at least one capability must be declared (use the empty list explicitly only for sandboxed test capsules)"))
	}
	for i, p := range m.Permissions {
		if p.Capability == "" {
			collect(fmt.Errorf("permissions[%d].capability is required", i))
			continue
		}
		// Reserved-namespace is a pure naming rule — runs even in
		// offline mode. The "is this capability actually registered?"
		// check requires a real runtime and runs only when reg != nil.
		if isReservedCapsuleNamespace(p.Capability) {
			collect(fmt.Errorf("permissions[%d]: capability %q is in a reserved namespace (audit.*, permission.*, pairing.* — runtime-only)", i, p.Capability))
		} else if reg != nil && !reg.HasCapability(p.Capability) {
			collect(fmt.Errorf("permissions[%d]: capability %q is not registered with this runtime", i, p.Capability))
		}
	}

	// --- tools -----------------------------------------------------
	if len(m.Tools) == 0 {
		collect(errors.New("tools: at least one tool must be declared (a capsule that exposes no tools has no surface)"))
	}
	toolNames := make(map[string]bool)
	for i, t := range m.Tools {
		if t.Name == "" {
			collect(fmt.Errorf("tools[%d].name is required", i))
			continue
		}
		if !toolNameRegex.MatchString(t.Name) {
			collect(fmt.Errorf("tools[%d].name %q invalid (alphanumeric, dots, underscores; must start with a letter)", i, t.Name))
		}
		if toolNames[t.Name] {
			collect(fmt.Errorf("tools[%d].name %q duplicates an earlier tool", i, t.Name))
		}
		toolNames[t.Name] = true
		// Shape check on input_schema: must be a JSON Schema-shaped
		// object (has "type" key). Full JSON Schema validation runs
		// at tool-invocation time; here we just guard against
		// authors who forgot the schema.
		if len(t.InputSchema) > 0 {
			if _, ok := t.InputSchema["type"]; !ok {
				collect(fmt.Errorf("tools[%d].input_schema must have a \"type\" field", i))
			}
			// Round-trip through JSON to confirm the schema is
			// JSON-encodable. Catches non-JSON-serializable values
			// (channels, funcs) that the YAML decoder permits.
			if _, err := json.Marshal(t.InputSchema); err != nil {
				collect(fmt.Errorf("tools[%d].input_schema is not JSON-serializable: %v", i, err))
			}
		}
	}

	// --- model_requirements ----------------------------------------
	if m.ModelRequirements.PreferredQuality != "" {
		switch m.ModelRequirements.PreferredQuality {
		case "high", "balanced", "fast":
			// valid
		default:
			collect(fmt.Errorf("model_requirements.preferred_quality %q invalid (must be high | balanced | fast)", m.ModelRequirements.PreferredQuality))
		}
	}
	if m.ModelRequirements.MinContextTokens < 0 {
		collect(fmt.Errorf("model_requirements.min_context_tokens must be >= 0, got %d", m.ModelRequirements.MinContextTokens))
	}

	// --- runtime ---------------------------------------------------
	switch m.Runtime.Type {
	case "":
		collect(errors.New("runtime.type is required"))
	case "subprocess":
		// supported
	case "wasm":
		collect(errors.New("runtime.type \"wasm\" is reserved but not yet supported by v0.1"))
	default:
		collect(fmt.Errorf("runtime.type %q unsupported (v0.1 accepts \"subprocess\")", m.Runtime.Type))
	}
	if len(m.Runtime.Entrypoint) == 0 {
		collect(errors.New("runtime.entrypoint is required (non-empty argv)"))
	}
	if m.Runtime.Protocol == "" {
		collect(errors.New("runtime.protocol is required"))
	} else if m.Runtime.Protocol != "mcp" {
		collect(fmt.Errorf("runtime.protocol %q unsupported (v0.1 accepts \"mcp\")", m.Runtime.Protocol))
	}
	if m.Runtime.Resources.MemoryMB < 0 {
		collect(fmt.Errorf("runtime.resources.memory_mb must be >= 0, got %d", m.Runtime.Resources.MemoryMB))
	}
	if m.Runtime.Resources.CPUQuota < 0 {
		collect(fmt.Errorf("runtime.resources.cpu_quota must be >= 0, got %v", m.Runtime.Resources.CPUQuota))
	}

	// --- roles -----------------------------------------------------
	rolesSet := make(map[string]bool, len(m.Roles))
	for i, role := range m.Roles {
		if !knownCapsuleRoles[role] {
			collect(fmt.Errorf("roles[%d]: %q is not a known role (ingestor | organizer | exposer | actuator)", i, role))
		}
		if rolesSet[role] {
			collect(fmt.Errorf("roles[%d]: %q is listed more than once", i, role))
		}
		rolesSet[role] = true
	}

	// --- ingestor --------------------------------------------------
	if rolesSet["ingestor"] && m.Ingestor == nil {
		collect(errors.New("ingestor: block is required when roles contains \"ingestor\""))
	}
	if m.Ingestor != nil {
		if !rolesSet["ingestor"] {
			collect(errors.New("ingestor: block is set but roles does not contain \"ingestor\""))
		}
		if m.Ingestor.SourceID == "" {
			collect(errors.New("ingestor.source_id is required"))
		} else if !sourceIDRegex.MatchString(m.Ingestor.SourceID) {
			collect(fmt.Errorf("ingestor.source_id %q invalid (must match %s)", m.Ingestor.SourceID, sourceIDRegex.String()))
		}
		if m.Ingestor.Schedule.Interval == "" {
			collect(errors.New("ingestor.schedule.interval is required"))
		} else if d, err := time.ParseDuration(m.Ingestor.Schedule.Interval); err != nil {
			collect(fmt.Errorf("ingestor.schedule.interval %q invalid: %v", m.Ingestor.Schedule.Interval, err))
		} else if d < ingestorMinInterval {
			collect(fmt.Errorf("ingestor.schedule.interval %v < min %v (faster cadences overwhelm typical provider APIs)", d, ingestorMinInterval))
		} else if d > ingestorMaxInterval {
			collect(fmt.Errorf("ingestor.schedule.interval %v > max %v (slower than this and the user should just sync on demand)", d, ingestorMaxInterval))
		}
		if m.Ingestor.Schedule.Initial != "" {
			if _, err := time.ParseDuration(m.Ingestor.Schedule.Initial); err != nil {
				collect(fmt.Errorf("ingestor.schedule.initial %q invalid: %v", m.Ingestor.Schedule.Initial, err))
			}
		}
		if m.Ingestor.OnTrigger == "" {
			collect(errors.New("ingestor.on_trigger is required (name of the capsule tool the scheduler invokes)"))
		} else if !toolNames[m.Ingestor.OnTrigger] {
			collect(fmt.Errorf("ingestor.on_trigger %q does not reference any declared tool", m.Ingestor.OnTrigger))
		}
	}

	// --- oauth -----------------------------------------------------
	if m.OAuth != nil {
		if m.OAuth.Provider == "" {
			collect(errors.New("oauth.provider is required"))
		}
		// Endpoints required only when the provider isn't in the
		// runtime's well-known registry. The registry ships with
		// google + github at minimum; non-well-known providers must
		// declare endpoints inline. WellKnownOAuthProvider is wired
		// from cli/start.go.
		wellKnown := m.OAuth.Provider != "" && WellKnownOAuthProvider(m.OAuth.Provider)
		if !wellKnown {
			if m.OAuth.AuthorizationEndpoint == "" {
				collect(errors.New("oauth.authorization_endpoint is required (provider is not in the well-known registry — must declare endpoints inline)"))
			} else if !strings.HasPrefix(m.OAuth.AuthorizationEndpoint, "https://") {
				collect(fmt.Errorf("oauth.authorization_endpoint %q must use https:// (auth codes and state leak over plaintext)", m.OAuth.AuthorizationEndpoint))
			}
			if m.OAuth.TokenEndpoint == "" {
				collect(errors.New("oauth.token_endpoint is required (provider is not in the well-known registry — must declare endpoints inline)"))
			} else if !strings.HasPrefix(m.OAuth.TokenEndpoint, "https://") {
				collect(fmt.Errorf("oauth.token_endpoint %q must use https://", m.OAuth.TokenEndpoint))
			}
		} else {
			// Well-known providers must NOT redeclare endpoints —
			// avoids confusion about whose URL wins. Manifest authors
			// override via ExtraParams, not endpoint replacement.
			if m.OAuth.AuthorizationEndpoint != "" {
				collect(fmt.Errorf("oauth.authorization_endpoint must NOT be set for well-known provider %q (the runtime supplies it)", m.OAuth.Provider))
			}
			if m.OAuth.TokenEndpoint != "" {
				collect(fmt.Errorf("oauth.token_endpoint must NOT be set for well-known provider %q (the runtime supplies it)", m.OAuth.Provider))
			}
		}
		if len(m.OAuth.Scopes) == 0 {
			collect(errors.New("oauth.scopes must declare at least one scope (so users see what's being requested at install)"))
		}
	}

	// --- memory_extensions -----------------------------------------
	if m.MemoryExtensions != nil {
		for i, et := range m.MemoryExtensions.EntityTypes {
			if et.Name == "" {
				collect(fmt.Errorf("memory_extensions.entity_types[%d].name is required", i))
			}
			if et.Namespace == "" {
				collect(fmt.Errorf("memory_extensions.entity_types[%d].namespace is required (reverse-DNS, e.g., com.acme.tax)", i))
			} else if !reverseDNSRegex.MatchString(et.Namespace) {
				collect(fmt.Errorf("memory_extensions.entity_types[%d].namespace %q is not reverse-DNS-shaped", i, et.Namespace))
			}
			if len(et.Schema) == 0 {
				collect(fmt.Errorf("memory_extensions.entity_types[%d].schema is required", i))
			} else if _, ok := et.Schema["type"]; !ok {
				collect(fmt.Errorf("memory_extensions.entity_types[%d].schema must have a \"type\" field", i))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// reservedCapsulePrefixes are the leading dot-separated prefixes
// capsules cannot declare capabilities under. Mirrors
// internal/permission/canonical.go::reservedNamespaces; the duplicate
// is intentional so the capsule package can validate manifests
// without importing permission (which would create an import
// chain that complicates capsule-side tooling like `capsule
// validate` running offline).
//
// MUST stay in sync with permission/canonical.go. The TestReservedListsInSync
// test in the permission package pins this.
var reservedCapsulePrefixes = []string{
	"runtime.",
	"loamss.",
	"audit.",
	"permission.",
	"pairing.",
}

// reservedCapsuleExceptions are capability names that look reserved
// but are explicitly allowed for capsules (e.g., audit.read so a
// capsule can introspect its own history).
var reservedCapsuleExceptions = map[string]bool{
	"audit.read": true,
}

func isReservedCapsuleNamespace(name string) bool {
	if reservedCapsuleExceptions[name] {
		return false
	}
	for _, prefix := range reservedCapsulePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Regexes used by Validate. Compiled once at package init.
var (
	nameRegex       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	toolNameRegex   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._]*$`)
	semverRegex     = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	reverseDNSRegex = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9-]*)+$`)
	sourceIDRegex   = regexp.MustCompile(`^source:[a-z][a-z0-9-]*$`)
)

// knownCapsuleRoles is the closed set of values allowed in
// manifest.roles. Matches extensibility.md §"the capsule taxonomy
// roles". The list is intentionally closed — adding a role is a
// spec-bearing decision, not something a capsule author should do.
var knownCapsuleRoles = map[string]bool{
	"ingestor":  true,
	"organizer": true,
	"exposer":   true,
	"actuator":  true,
}

// ingestor schedule bounds.
const (
	ingestorMinInterval = time.Minute
	ingestorMaxInterval = 24 * time.Hour
)
