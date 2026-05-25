package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// The credentials.* tools let capsule ingestors persist long-lived
// secrets (OAuth refresh tokens, API keys) via the runtime's
// configured storage adapter — the same encryption-at-rest path
// in-tree source connectors use today.
//
// Capsule-namespacing is enforced by construction: the handler reads
// the capsule's name from the authenticated Principal and never
// trusts the args to identify the capsule. A misbehaving capsule
// cannot read another capsule's keys, nor sneak a key under a
// different capsule's namespace.
//
// All three tools refuse non-capsule callers (PrincipalClient,
// future principal kinds) — even though external clients with the
// credentials.* capabilities would technically authorize the call,
// the storage shape is capsule-keyed and there'd be nowhere to put
// an external client's blob. This is enforced via PrincipalKind, not
// scope.

// credentialsSetArgs is the input shape for credentials.set.
type credentialsSetArgs struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// credentialsKeyArgs is the input shape shared by get/delete.
type credentialsKeyArgs struct {
	Key string `json:"key"`
}

// credentialsGetResult is what credentials.get returns. When the key
// is absent or expired, `Found` is false and `Value` is empty.
type credentialsGetResult struct {
	Found     bool   `json:"found"`
	Value     string `json:"value,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// NewCredentialsSetTool builds credentials.set.
func NewCredentialsSetTool(store *CapsuleCredentialStore, aud audit.Writer) Tool {
	return Tool{
		Name: "credentials.set",
		Description: "Store an encrypted credential, scoped to this capsule installation. " +
			"Optional expires_at marks the value as stale after that time (reads return found=false). " +
			"Only callable by capsule principals.",
		Capability: "credentials.write",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "key":        {"type": "string", "pattern": "^[a-zA-Z0-9_.-]+$"},
                "value":      {"type": "string"},
                "expires_at": {"type": "string", "format": "date-time"}
            },
            "required": ["key", "value"],
            "additionalProperties": false
        }`),
		Handler: makeCredentialsSetHandler(store, aud),
	}
}

func makeCredentialsSetHandler(
	store *CapsuleCredentialStore, aud audit.Writer,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipal(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		var args credentialsSetArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Key == "" {
			return ToolResult{}, errors.New("invalid arguments: key is required")
		}
		var expires *time.Time
		if args.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, args.ExpiresAt)
			if err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: expires_at must be RFC3339: %w", err)
			}
			expires = &t
		}
		if err := store.Set(ctx, capsuleName, args.Key, args.Value, expires); err != nil {
			return ToolResult{}, fmt.Errorf("%w: credentials.set: %v", ErrToolBackend, err)
		}
		recordCredentialOp(ctx, aud, "credentials.set", capsuleName, args.Key, in.GrantID)
		content, err := JSONContent(map[string]any{"ok": true})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// NewCredentialsGetTool builds credentials.get.
func NewCredentialsGetTool(store *CapsuleCredentialStore, aud audit.Writer) Tool {
	return Tool{
		Name: "credentials.get",
		Description: "Read a previously-set credential for this capsule installation. " +
			"Returns found=false when the key is absent or expired. " +
			"Only callable by capsule principals.",
		Capability: "credentials.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "key": {"type": "string", "pattern": "^[a-zA-Z0-9_.-]+$"}
            },
            "required": ["key"],
            "additionalProperties": false
        }`),
		Handler: makeCredentialsGetHandler(store, aud),
	}
}

func makeCredentialsGetHandler(
	store *CapsuleCredentialStore, aud audit.Writer,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipal(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		var args credentialsKeyArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Key == "" {
			return ToolResult{}, errors.New("invalid arguments: key is required")
		}
		entry, found, err := store.Get(ctx, capsuleName, args.Key)
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: credentials.get: %v", ErrToolBackend, err)
		}
		out := credentialsGetResult{Found: found}
		if found {
			out.Value = entry.Value
			if entry.ExpiresAt != nil {
				out.ExpiresAt = entry.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		recordCredentialOp(ctx, aud, "credentials.get", capsuleName, args.Key, in.GrantID)
		content, err := JSONContent(out)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// NewCredentialsDeleteTool builds credentials.delete.
func NewCredentialsDeleteTool(store *CapsuleCredentialStore, aud audit.Writer) Tool {
	return Tool{
		Name: "credentials.delete",
		Description: "Remove a credential for this capsule installation. " +
			"Idempotent — deleting a missing key succeeds. " +
			"Only callable by capsule principals.",
		Capability: "credentials.write",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "key": {"type": "string", "pattern": "^[a-zA-Z0-9_.-]+$"}
            },
            "required": ["key"],
            "additionalProperties": false
        }`),
		Handler: makeCredentialsDeleteHandler(store, aud),
	}
}

func makeCredentialsDeleteHandler(
	store *CapsuleCredentialStore, aud audit.Writer,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipal(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		var args credentialsKeyArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Key == "" {
			return ToolResult{}, errors.New("invalid arguments: key is required")
		}
		if err := store.Delete(ctx, capsuleName, args.Key); err != nil {
			return ToolResult{}, fmt.Errorf("%w: credentials.delete: %v", ErrToolBackend, err)
		}
		recordCredentialOp(ctx, aud, "credentials.delete", capsuleName, args.Key, in.GrantID)
		content, err := JSONContent(map[string]any{"ok": true})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// requireCapsulePrincipal returns the capsule name from an
// authenticated capsule principal, or an error when the caller is
// not a capsule. External clients with credentials.* capabilities
// can't use these tools — there's no per-client storage slot, and
// allowing it would silently mis-key data under the client id.
func requireCapsulePrincipal(p permission.Principal) (string, error) {
	if p.Kind != permission.PrincipalCapsule {
		return "", fmt.Errorf("credentials.* tools are restricted to capsule principals (caller is %s)", p.Kind)
	}
	if p.ID == "" {
		return "", ErrEmptyCapsuleName
	}
	return p.ID, nil
}

// recordCredentialOp emits a per-operation audit entry. Distinct
// from the dispatcher's tool.invoked entry so consumers can filter
// for credential touches specifically and see the key (never the
// value) that was set/got/deleted.
func recordCredentialOp(
	ctx context.Context, aud audit.Writer, op, capsuleName, key, grantID string,
) {
	entry := audit.Entry{
		Type:    op,
		Actor:   audit.Actor{Kind: audit.ActorCapsule, ID: capsuleName},
		Subject: &audit.Subject{Kind: audit.SubjectTool, ID: op},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"capsule": capsuleName,
			"key":     key,
		},
	}
	if grantID != "" {
		entry.Data["grant_id"] = grantID
	}
	_, _ = aud.Append(ctx, entry)
}
