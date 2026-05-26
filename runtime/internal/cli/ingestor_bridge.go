package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/source"
)

// daemonIngestorBridge is the concrete capsule.IngestorBridge wired
// in the daemon's start path. It composes the three runtime stores
// an ingestor capsule's install/uninstall must touch:
//
//   - source.Store: the visible sources-table row (so the capsule
//     appears in `loamss source list` and the dashboard)
//   - mcp.CapsuleCredentialStore: per-capsule encrypted credential
//     blobs (cleaned up on uninstall so the capsule's secrets don't
//     outlive it)
//   - mcp.CapsuleCursorStore: per-capsule sync cursor (same lifetime)
//
// Lives in the cli package — not the capsule package — to keep the
// capsule package import surface free of source/mcp dependencies.
type daemonIngestorBridge struct {
	sources *source.Store
	creds   *mcp.CapsuleCredentialStore
	cursor  *mcp.CapsuleCursorStore
}

func newDaemonIngestorBridge(
	sources *source.Store,
	creds *mcp.CapsuleCredentialStore,
	cursor *mcp.CapsuleCursorStore,
) *daemonIngestorBridge {
	return &daemonIngestorBridge{sources: sources, creds: creds, cursor: cursor}
}

// OnInstall inserts a sources-table row when the capsule declares
// the ingestor role. No-op otherwise. Returning a non-nil error
// rolls back the capsule install (per the contract in
// capsule.IngestorBridge).
func (b *daemonIngestorBridge) OnInstall(ctx context.Context, c *capsule.Installed) error {
	if !hasIngestorRole(c) {
		return nil
	}
	spec := c.Manifest.Ingestor
	if spec == nil {
		// Validate should have caught this — defense in depth.
		return errors.New("ingestor bridge: capsule has ingestor role but no ingestor block")
	}
	row := source.Configured{
		Name:         c.Name,
		AdapterID:    spec.SourceID,
		Config:       map[string]any{},
		OwnerCapsule: c.Name,
	}
	if _, err := b.sources.Insert(ctx, row); err != nil {
		return fmt.Errorf("inserting sources row for ingestor capsule %s: %w", c.Name, err)
	}
	return nil
}

// OnUninstall removes the capsule's sources-table row + cleans up
// its credential and cursor blobs. Errors are returned for the
// installer to log in the uninstall audit entry; they do not roll
// back the uninstall.
func (b *daemonIngestorBridge) OnUninstall(ctx context.Context, name string) error {
	var errs []error

	// Source row removal — only matches if the capsule was an
	// ingestor in the first place. ErrSourceNotFound is the expected
	// case for non-ingestor capsules and gets swallowed here.
	if err := b.sources.Delete(ctx, name); err != nil && !errors.Is(err, source.ErrSourceNotFound) {
		errs = append(errs, fmt.Errorf("deleting sources row: %w", err))
	}

	if err := b.creds.DeleteAll(ctx, name); err != nil {
		errs = append(errs, fmt.Errorf("clearing credentials: %w", err))
	}
	if err := b.cursor.DeleteAll(ctx, name); err != nil {
		errs = append(errs, fmt.Errorf("clearing cursor: %w", err))
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func hasIngestorRole(c *capsule.Installed) bool {
	if c == nil || c.Manifest == nil {
		return false
	}
	for _, r := range c.Manifest.Roles {
		if r == "ingestor" {
			return true
		}
	}
	return false
}
