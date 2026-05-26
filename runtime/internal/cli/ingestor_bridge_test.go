package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// inMemStorageForBridge is a minimal storage.Adapter used by the
// bridge tests. Mirrors the mcp package's test fake but lives here
// to avoid coupling to mcp_test internals.
type inMemStorageForBridge struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newBridgeStorage() *inMemStorageForBridge {
	return &inMemStorageForBridge{files: map[string][]byte{}}
}

func (s *inMemStorageForBridge) Init(_ context.Context, _ map[string]any) error { return nil }
func (s *inMemStorageForBridge) Close(_ context.Context) error                  { return nil }
func (s *inMemStorageForBridge) HealthCheck(_ context.Context) error            { return nil }

func (s *inMemStorageForBridge) Read(_ context.Context, p string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[p]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}
func (s *inMemStorageForBridge) Write(_ context.Context, p string, c []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[p] = append([]byte(nil), c...)
	return nil
}
func (s *inMemStorageForBridge) Delete(_ context.Context, p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, p)
	return nil
}
func (s *inMemStorageForBridge) Exists(_ context.Context, p string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[p]
	return ok, nil
}
func (s *inMemStorageForBridge) ReadStream(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
	return nil, storage.ErrUnsupported
}
func (s *inMemStorageForBridge) WriteStream(_ context.Context, _ string, _ io.Reader) error {
	return storage.ErrUnsupported
}
func (s *inMemStorageForBridge) Metadata(_ context.Context, _ string) (storage.ObjectMetadata, error) {
	return storage.ObjectMetadata{}, storage.ErrUnsupported
}
func (s *inMemStorageForBridge) List(_ context.Context, _ string) (<-chan storage.ListEntry, error) {
	return nil, storage.ErrUnsupported
}
func (s *inMemStorageForBridge) SignedURL(_ context.Context, _ string, _ time.Duration, _ storage.Op) (string, error) {
	return "", storage.ErrUnsupported
}

// bridgeFixture wires every dependency the bridge tests need:
// runtime.db (permission + capsule + source stores), audit.db,
// in-memory storage, real credential + cursor stores, the
// installer with the daemon bridge wired up.
type bridgeFixture struct {
	t         *testing.T
	dir       string
	storage   *inMemStorageForBridge
	srcStore  *source.Store
	creds     *mcp.CapsuleCredentialStore
	cursor    *mcp.CapsuleCursorStore
	installer *capsule.Installer
}

func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	dbPath := filepath.Join(dir, "runtime.db")

	permStore, err := permission.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	capStore, err := capsule.OpenStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("capsule.OpenStore: %v", err)
	}
	srcStore, err := source.OpenStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("source.OpenStore: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = permStore.Close()
		_ = capStore.Close()
		_ = srcStore.Close()
		_ = w.Close(context.Background())
	})
	engine := permission.NewEngine(permStore, w)

	stor := newBridgeStorage()
	creds := mcp.NewCapsuleCredentialStore(stor)
	cursor := mcp.NewCapsuleCursorStore(stor)
	bridge := newDaemonIngestorBridge(srcStore, creds, cursor)
	inst := capsule.NewInstaller(capStore, engine, w, filepath.Join(dir, "capsules")).
		SetIngestorBridge(bridge)

	return &bridgeFixture{
		t: t, dir: dir, storage: stor, srcStore: srcStore,
		creds: creds, cursor: cursor, installer: inst,
	}
}

// fixturePathFromCapsuleTestdata materializes the named fixture file
// from the capsule package's testdata/ into a per-test directory the
// installer can read. Mirrors the helper in capsule/installer_test.go.
func fixturePathFromCapsuleTestdata(t *testing.T, basename string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "capsule", "testdata", basename)
	dst := filepath.Join(dir, basename)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", basename, err)
	}
	if err := os.WriteFile(filepath.Join(dst, "capsule.yaml"), data, 0o600); err != nil {
		t.Fatalf("write capsule.yaml: %v", err)
	}
	// Add a stub code file so copyTree has something to do.
	codeDir := filepath.Join(dst, "code")
	if err := os.MkdirAll(codeDir, 0o700); err != nil {
		t.Fatalf("mkdir code: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codeDir, "server.js"), []byte("// stub\n"), 0o600); err != nil {
		t.Fatalf("write server.js: %v", err)
	}
	return dst
}

// --- Tests --------------------------------------------------------

func TestIngestorBridge_InstallCreatesSourceRow(t *testing.T) {
	f := newBridgeFixture(t)
	ctx := context.Background()

	src := fixturePathFromCapsuleTestdata(t, "valid-calendar-ingestor.yaml")
	res, err := f.installer.Install(ctx, src, "user")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Capsule.Name != "calendar-ingestor" {
		t.Errorf("capsule name: %q", res.Capsule.Name)
	}

	// Sources row should exist with the capsule's declared source_id
	// and OwnerCapsule set.
	row, err := f.srcStore.Get(ctx, "calendar-ingestor")
	if err != nil {
		t.Fatalf("Get sources row: %v", err)
	}
	if row.AdapterID != "source:calendar" {
		t.Errorf("adapter_id: %q", row.AdapterID)
	}
	if row.OwnerCapsule != "calendar-ingestor" {
		t.Errorf("owner_capsule: %q", row.OwnerCapsule)
	}
}

func TestIngestorBridge_InstallSkipsNonIngestorCapsule(t *testing.T) {
	f := newBridgeFixture(t)
	ctx := context.Background()

	src := fixturePathFromCapsuleTestdata(t, "valid-email-drafter.yaml")
	if _, err := f.installer.Install(ctx, src, "user"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// No sources row should have been created.
	if _, err := f.srcStore.Get(ctx, "email-drafter"); !errors.Is(err, source.ErrSourceNotFound) {
		t.Errorf("expected no sources row for non-ingestor capsule, got: %v", err)
	}
}

func TestIngestorBridge_UninstallRemovesSourceRow(t *testing.T) {
	f := newBridgeFixture(t)
	ctx := context.Background()

	src := fixturePathFromCapsuleTestdata(t, "valid-calendar-ingestor.yaml")
	if _, err := f.installer.Install(ctx, src, "user"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Seed a credential + cursor so we can verify cleanup.
	if err := f.creds.Set(ctx, "calendar-ingestor", "refresh_token", "secret", nil); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	if err := f.cursor.Set(ctx, "calendar-ingestor", "highwater"); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	if err := f.installer.Uninstall(ctx, "calendar-ingestor", "user", "test"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// Sources row gone.
	if _, err := f.srcStore.Get(ctx, "calendar-ingestor"); !errors.Is(err, source.ErrSourceNotFound) {
		t.Errorf("expected sources row removed, got: %v", err)
	}
	// Credentials cleared.
	_, found, _ := f.creds.Get(ctx, "calendar-ingestor", "refresh_token")
	if found {
		t.Error("credentials should be cleared on uninstall")
	}
	// Cursor cleared.
	cur, _ := f.cursor.Get(ctx, "calendar-ingestor")
	if cur != "" {
		t.Errorf("cursor should be cleared on uninstall, got %q", cur)
	}
}

func TestIngestorBridge_UninstallNonIngestorIsNoOpForSources(t *testing.T) {
	f := newBridgeFixture(t)
	ctx := context.Background()

	src := fixturePathFromCapsuleTestdata(t, "valid-email-drafter.yaml")
	if _, err := f.installer.Install(ctx, src, "user"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Should succeed even with no sources row to delete.
	if err := f.installer.Uninstall(ctx, "email-drafter", "user", ""); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
}

func TestSourceBuild_ShortCircuitsForCapsuleOwnedRow(t *testing.T) {
	// Confirms source.Build refuses to dispatch a capsule-owned row
	// (step 5 of the RFC is where scheduled triggers wire up).
	f := newBridgeFixture(t)
	ctx := context.Background()
	src := fixturePathFromCapsuleTestdata(t, "valid-calendar-ingestor.yaml")
	if _, err := f.installer.Install(ctx, src, "user"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	row, _ := f.srcStore.Get(ctx, "calendar-ingestor")
	_, err := source.Build(ctx, source.BuildEnv{Storage: f.storage}, row)
	if !errors.Is(err, source.ErrCapsuleIngestorNotYetExecutable) {
		t.Errorf("expected ErrCapsuleIngestorNotYetExecutable, got: %v", err)
	}
}
