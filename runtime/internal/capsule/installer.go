package capsule

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Installer is the high-level "do everything needed to install a
// capsule" operation. It composes the manifest parser/validator,
// the on-disk code copy, the grant-issuance loop, and the audit
// emission into one transactional surface.
//
// Transactional in the soft sense: each step is independent
// (manifest parsed, code copied, grants issued, record persisted),
// and a failure mid-way leaves the system in a defined state we
// can clean up:
//
//   - manifest fail   → nothing on disk, no grants, no record
//   - code-copy fail  → manifest parsed but no code, no grants, no record
//   - grant fail      → code on disk, partial grants. We roll back by
//     revoking any grants issued so far and deleting
//     the copied code dir.
//   - persist fail    → grants issued + code copied. Same rollback as
//     grant fail.
//
// True ACID across heterogeneous stores (SQLite for grants, filesystem
// for code) isn't possible without a journal; the rollback path
// covers the common failure modes.
type Installer struct {
	store      *Store
	engine     *permission.Engine
	audit      audit.Writer
	installDir string // root under which per-capsule directories are created
}

// NewInstaller constructs an Installer. installDir is typically
// <data_dir>/capsules; the installer creates it lazily.
func NewInstaller(store *Store, engine *permission.Engine, w audit.Writer, installDir string) *Installer {
	return &Installer{store: store, engine: engine, audit: w, installDir: installDir}
}

// InstallResult is the summary returned by Install. Surfaced to the
// CLI so the user sees what was created.
type InstallResult struct {
	Capsule  *Installed
	GrantIDs []string
}

// Install runs the full install pipeline:
//
//  1. Parse + validate the manifest at sourcePath
//     (sourcePath may be a directory or a .yaml file).
//  2. Copy the capsule's code into <installDir>/<name>@<version>/.
//     If sourcePath was a bare .yaml, no code copy is performed
//     (test capsules, manifest-only).
//  3. Issue a grant for each declared permission, attached to
//     Principal{Kind: PrincipalCapsule, ID: name}. Each grant
//     emits its own grant.create audit entry via the engine.
//  4. Persist the Installed record.
//  5. Emit a capsule.installed audit entry.
//
// Rolls back on partial failure (revokes any issued grants, removes
// the copied code) so the user can retry cleanly.
func (i *Installer) Install(ctx context.Context, sourcePath, installedBy string) (*InstallResult, error) {
	// --- 1. Parse + validate -------------------------------------
	manifest, err := i.loadAndValidate(ctx, sourcePath)
	if err != nil {
		return nil, err
	}

	// Reject re-install (name collision); upgrades come in a later
	// commit. We check up front so we don't pollute the filesystem
	// before discovering the conflict.
	if existing, err := i.store.Get(ctx, manifest.Name); err == nil {
		return nil, fmt.Errorf("%w: %s (version %s installed at %s)",
			ErrCapsuleAlreadyInstalled, existing.Name, existing.Version,
			existing.InstalledAt.UTC().Format(time.RFC3339))
	} else if !errors.Is(err, ErrCapsuleNotFound) {
		return nil, err
	}

	// --- 2. Copy code --------------------------------------------
	installPath, err := i.copyCode(sourcePath, manifest)
	if err != nil {
		return nil, err
	}

	// --- 3. Issue grants -----------------------------------------
	rollback := []string{} // grant IDs issued so far; revoke on failure
	rollbackCode := func() { _ = os.RemoveAll(installPath) }
	rollbackGrants := func() {
		for _, id := range rollback {
			_ = i.engine.RevokeGrant(ctx, id, installedBy, "rolling back failed capsule install")
		}
	}

	for idx, p := range manifest.Permissions {
		issued, err := i.engine.IssueGrant(ctx, permission.Grant{
			Principal: permission.Principal{
				Kind: permission.PrincipalCapsule,
				ID:   manifest.Name,
			},
			Capability:           p.Capability,
			Scope:                p.Scope,
			Rationale:            p.Rationale,
			RequiresUserApproval: p.RequiresUserApproval,
		}, installedBy)
		if err != nil {
			rollbackGrants()
			rollbackCode()
			return nil, fmt.Errorf("issuing grant for permissions[%d] (%s): %w",
				idx, p.Capability, err)
		}
		rollback = append(rollback, issued.ID)
	}

	// --- 4. Persist record ---------------------------------------
	c := Installed{
		Name:        manifest.Name,
		Version:     manifest.Version,
		SpecVersion: manifest.SpecVersion,
		AuthorName:  manifest.Author.Name,
		AuthorURL:   manifest.Author.URL,
		Manifest:    manifest,
		InstallPath: installPath,
		InstalledAt: time.Now().UTC(),
	}
	c.ID = c.Name + "@" + c.Version
	if err := i.store.Insert(ctx, c); err != nil {
		rollbackGrants()
		rollbackCode()
		return nil, fmt.Errorf("persisting capsule record: %w", err)
	}

	// --- 5. Audit ------------------------------------------------
	entry := audit.Entry{
		Type:    "capsule.installed",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: defaultedActor(installedBy)},
		Subject: &audit.Subject{Kind: audit.SubjectCapsule, ID: c.Name},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"version":       c.Version,
			"spec_version":  c.SpecVersion,
			"author":        c.AuthorName,
			"grants_issued": len(rollback),
			"tools":         len(manifest.Tools),
			"install_path":  installPath,
		},
	}
	_, _ = i.audit.Append(ctx, entry)

	return &InstallResult{Capsule: &c, GrantIDs: rollback}, nil
}

// Uninstall removes a capsule:
//
//  1. Cascade-revoke every active grant for
//     Principal{Kind: PrincipalCapsule, ID: name} (each emits its
//     own grant.revoke entry via the engine).
//  2. Delete the capsule record.
//  3. Remove the installed code directory.
//  4. Emit capsule.uninstalled.
//
// Idempotent in the spirit of `loamss client revoke` — a missing
// capsule returns ErrCapsuleNotFound; partial state (record present,
// code missing) is handled gracefully.
func (i *Installer) Uninstall(ctx context.Context, name, uninstalledBy, reason string) error {
	c, err := i.store.Get(ctx, name)
	if err != nil {
		return err
	}

	// 1. Cascade-revoke active grants for this capsule.
	grants, err := i.engine.Store().ListGrants(ctx, permission.GrantFilter{
		PrincipalKind: permission.PrincipalCapsule,
		PrincipalID:   name,
		Status:        permission.StatusActive,
		Limit:         10_000,
	})
	if err != nil {
		return fmt.Errorf("listing grants for capsule %s: %w", name, err)
	}
	for _, g := range grants {
		cascReason := reason
		if cascReason == "" {
			cascReason = "capsule " + name + " uninstalled"
		} else {
			cascReason = "capsule " + name + " uninstalled: " + reason
		}
		if err := i.engine.RevokeGrant(ctx, g.ID, uninstalledBy, cascReason); err != nil {
			return fmt.Errorf("revoking grant %s: %w", g.ID, err)
		}
	}

	// 2. Delete record.
	if err := i.store.Delete(ctx, name); err != nil {
		return err
	}

	// 3. Remove code dir (best-effort; missing dir is fine).
	_ = os.RemoveAll(c.InstallPath)

	// 4. Audit.
	entry := audit.Entry{
		Type:    "capsule.uninstalled",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: defaultedActor(uninstalledBy)},
		Subject: &audit.Subject{Kind: audit.SubjectCapsule, ID: name},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"version":        c.Version,
			"grants_revoked": len(grants),
		},
	}
	if reason != "" {
		entry.Data["reason"] = reason
	}
	_, _ = i.audit.Append(ctx, entry)

	return nil
}

// --- helpers -----------------------------------------------------------

// loadAndValidate reads the manifest at sourcePath (file or
// directory), parses, and validates against the runtime's capability
// registry. The registry is the live permission store — capsule
// install MUST run online; we don't trust offline-validated
// manifests at install time.
func (i *Installer) loadAndValidate(ctx context.Context, sourcePath string) (*Manifest, error) {
	manifestPath := sourcePath
	if st, err := os.Stat(sourcePath); err == nil && st.IsDir() {
		manifestPath = filepath.Join(sourcePath, "capsule.yaml")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	// engine.Store() is the live capability registry. Wrap as the
	// CapabilityRegistry shape Validate expects.
	reg := storeRegistryAdapter{store: i.engine.Store(), ctx: ctx}
	if err := m.Validate(reg); err != nil {
		return nil, fmt.Errorf("manifest validation failed: %w", err)
	}
	return m, nil
}

// copyCode duplicates the capsule's code/ and assets/ subdirectories
// into <installDir>/<name>@<version>/. Returns the absolute
// installation path. When sourcePath is a bare .yaml file (no
// surrounding directory), no code copy is performed — the caller
// gets back an InstallPath pointing at a freshly-created empty
// directory. Test capsules + manifest-only smoke tests rely on this.
func (i *Installer) copyCode(sourcePath string, m *Manifest) (string, error) {
	if err := os.MkdirAll(i.installDir, 0o700); err != nil {
		return "", fmt.Errorf("creating install dir: %w", err)
	}
	installPath := filepath.Join(i.installDir, m.Name+"@"+m.Version)
	if err := os.MkdirAll(installPath, 0o700); err != nil {
		return "", fmt.Errorf("creating per-capsule dir: %w", err)
	}

	st, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		// File path: copy the manifest alone. No code/ to copy.
		dst := filepath.Join(installPath, "capsule.yaml")
		if err := copyFile(sourcePath, dst); err != nil {
			return "", err
		}
		return installPath, nil
	}

	// Directory path: copy capsule.yaml + code/ + assets/. Other
	// files are ignored.
	for _, sub := range []string{"capsule.yaml", "code", "assets"} {
		src := filepath.Join(sourcePath, sub)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		dst := filepath.Join(installPath, sub)
		if err := copyTree(src, dst); err != nil {
			return "", fmt.Errorf("copying %s: %w", sub, err)
		}
	}
	return installPath, nil
}

// copyTree walks src and duplicates everything to dst. Preserves
// file mode on a best-effort basis; ownership is dropped (the
// runtime runs as a single user). Symlinks are followed once.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		return copyFile(path, target)
	})
}

// copyFile copies one file from src to dst, creating dst's parent
// directory if needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// storeRegistryAdapter wraps a permission.Store to satisfy
// CapabilityRegistry. Used internally by loadAndValidate so the
// install path checks against the live registry.
type storeRegistryAdapter struct {
	store *permission.Store
	ctx   context.Context //nolint:containedctx // adapter lifetime matches one Install call
}

func (a storeRegistryAdapter) HasCapability(name string) bool {
	_, err := a.store.GetCapability(a.ctx, name)
	return err == nil
}

func defaultedActor(s string) string {
	if s == "" {
		return "user"
	}
	return s
}
