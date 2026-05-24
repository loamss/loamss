package fsencrypted

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// newAdapter constructs and initializes an fs-encrypted adapter rooted
// at a unique temp dir per test.
func newAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	root := t.TempDir()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"root": root}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return a, root
}

func TestInit_CreatesRootAndKey(t *testing.T) {
	root := t.TempDir()
	subroot := filepath.Join(root, "data") // does not exist initially

	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"root": subroot}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(subroot); err != nil {
		t.Errorf("root not created: %v", err)
	}
	keyPath := filepath.Join(subroot, metaDir, keyFile)
	stat, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if stat.Size() != int64(keySize) {
		t.Errorf("key size: got %d, want %d", stat.Size(), keySize)
	}
	// Permissions check: on Unix the key file should be 0600. We allow
	// at most owner-rw; group/world bits should be off.
	if mode := stat.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("key file mode %o: group/other bits should be zero", mode)
	}
}

func TestInit_ReusesExistingKey(t *testing.T) {
	root := t.TempDir()

	a1 := &Adapter{}
	if err := a1.Init(context.Background(), map[string]any{"root": root}); err != nil {
		t.Fatalf("Init #1: %v", err)
	}

	// Write a fixture; close adapter; reinit with a new adapter at the
	// same root; read it back. If the key is regenerated, decryption
	// fails — proving the key is persisted and reused.
	const payload = "hello world"
	if err := a1.Write(context.Background(), "fixture.txt", []byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = a1.Close(context.Background())

	a2 := &Adapter{}
	if err := a2.Init(context.Background(), map[string]any{"root": root}); err != nil {
		t.Fatalf("Init #2: %v", err)
	}
	got, err := a2.Read(context.Background(), "fixture.txt")
	if err != nil {
		t.Fatalf("Read after re-init: %v", err)
	}
	if string(got) != payload {
		t.Errorf("payload: got %q, want %q", got, payload)
	}
}

func TestInit_MissingRoot(t *testing.T) {
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestInit_BadRootType(t *testing.T) {
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"root": 42}); err == nil {
		t.Fatal("expected error for non-string root")
	}
}

func TestWriteRead_RoundTrip(t *testing.T) {
	a, _ := newAdapter(t)

	cases := []struct {
		name string
		body []byte
	}{
		{"small", []byte("hello world")},
		{"empty", []byte{}},
		{"binary", []byte{0, 1, 2, 3, 0xff, 0xfe, 0xfd}},
		{"medium", bytes.Repeat([]byte("loam "), 200)},
		{"unicode", []byte("こんにちは 🌱")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := "round/" + tc.name + ".bin"
			if err := a.Write(context.Background(), p, tc.body); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := a.Read(context.Background(), p)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !bytes.Equal(got, tc.body) {
				t.Errorf("round-trip mismatch: got %x, want %x", got, tc.body)
			}
		})
	}
}

func TestRead_NotFound(t *testing.T) {
	a, _ := newAdapter(t)
	_, err := a.Read(context.Background(), "no/such.txt")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestWrite_DifferentNoncesEachTime(t *testing.T) {
	a, root := newAdapter(t)
	const p = "nonce-check.bin"
	body := []byte("identical content each write")

	if err := a.Write(context.Background(), p, body); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	enc1, _ := os.ReadFile(filepath.Join(root, p))

	if err := a.Write(context.Background(), p, body); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	enc2, _ := os.ReadFile(filepath.Join(root, p))

	// Same plaintext + same key + fresh nonce → different ciphertext.
	// (Specifically: the nonce bytes differ, so the rest of the file
	// also differs since GCM mixes the nonce into every block.)
	if bytes.Equal(enc1, enc2) {
		t.Errorf("two writes of identical content produced identical ciphertext — nonces are not being randomized")
	}
}

func TestRead_RejectsTamperedFile(t *testing.T) {
	a, root := newAdapter(t)
	const p = "tampered.txt"
	if err := a.Write(context.Background(), p, []byte("intact content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Flip a byte in the ciphertext (after the header).
	abs := filepath.Join(root, p)
	data, _ := os.ReadFile(abs)
	if len(data) <= headerOverhead {
		t.Fatalf("file too short to tamper: %d bytes", len(data))
	}
	data[headerOverhead] ^= 0xff
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	_, err := a.Read(context.Background(), p)
	if err == nil {
		t.Fatal("expected GCM auth failure, got nil")
	}
	// We don't require a specific sentinel — just that it doesn't
	// silently return tampered plaintext.
}

func TestDelete_Idempotent(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.Write(context.Background(), "delete-me.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := a.Delete(context.Background(), "delete-me.txt"); err != nil {
		t.Fatalf("Delete first call: %v", err)
	}
	if err := a.Delete(context.Background(), "delete-me.txt"); err != nil {
		t.Errorf("Delete second call should be idempotent, got: %v", err)
	}
	if err := a.Delete(context.Background(), "never-existed.txt"); err != nil {
		t.Errorf("Delete of missing object should be nil, got: %v", err)
	}
}

func TestExists(t *testing.T) {
	a, _ := newAdapter(t)
	got, err := a.Exists(context.Background(), "absent.txt")
	if err != nil || got {
		t.Errorf("Exists(absent): got (%v, %v); want (false, nil)", got, err)
	}
	if err := a.Write(context.Background(), "present.txt", []byte("yes")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err = a.Exists(context.Background(), "present.txt")
	if err != nil || !got {
		t.Errorf("Exists(present): got (%v, %v); want (true, nil)", got, err)
	}
}

func TestMetadata_BasicFields(t *testing.T) {
	a, _ := newAdapter(t)
	body := bytes.Repeat([]byte("xy"), 100)
	if err := a.Write(context.Background(), "doc.json", body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	md, err := a.Metadata(context.Background(), "doc.json")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if md.Path != "doc.json" {
		t.Errorf("Path: %q", md.Path)
	}
	if md.Size != int64(len(body)) {
		t.Errorf("Size: got %d, want %d (content size, not encrypted-file size)", md.Size, len(body))
	}
	if md.ContentType == "" {
		t.Errorf("ContentType should be set for .json extension")
	}
	if md.ModTime.IsZero() {
		t.Errorf("ModTime should be set")
	}
}

func TestMetadata_NotFound(t *testing.T) {
	a, _ := newAdapter(t)
	_, err := a.Metadata(context.Background(), "absent.txt")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestPathSafety(t *testing.T) {
	a, _ := newAdapter(t)
	cases := []struct {
		name string
		path string
	}{
		{"absolute", "/etc/passwd"},
		{"traversal", "../../etc/passwd"},
		{"embedded traversal", "ok/../../bad"},
		{"meta reserved", metaDir + "/master.key"},
		{"null byte", "evil\x00path.txt"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.Write(context.Background(), tc.path, []byte("nope")); err == nil {
				t.Errorf("Write should have rejected %q", tc.path)
			}
			if _, err := a.Read(context.Background(), tc.path); err == nil {
				t.Errorf("Read should have rejected %q", tc.path)
			}
		})
	}
}

func TestList_VisibleAndExcludesMeta(t *testing.T) {
	a, _ := newAdapter(t)
	files := []string{
		"a.txt",
		"sub/b.txt",
		"sub/deep/c.txt",
		"other/d.txt",
	}
	for _, p := range files {
		if err := a.Write(context.Background(), p, []byte("x")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	collect := func(prefix string) (paths []string, errs []error) {
		ch, err := a.List(context.Background(), prefix)
		if err != nil {
			t.Fatalf("List(%q): %v", prefix, err)
		}
		for entry := range ch {
			if entry.Err != nil {
				errs = append(errs, entry.Err)
				continue
			}
			paths = append(paths, entry.Metadata.Path)
		}
		return paths, errs
	}

	gotAll, errs := collect("")
	if len(errs) > 0 {
		t.Fatalf("List errors: %v", errs)
	}
	for _, want := range files {
		found := false
		for _, got := range gotAll {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("List missing %q; got %v", want, gotAll)
		}
	}
	// Meta key must not appear.
	for _, got := range gotAll {
		if strings.HasPrefix(got, metaDir) {
			t.Errorf("List exposed meta path: %q", got)
		}
	}

	gotPrefixed, errs := collect("sub")
	if len(errs) > 0 {
		t.Fatalf("List errors: %v", errs)
	}
	for _, got := range gotPrefixed {
		if !strings.HasPrefix(got, "sub/") {
			t.Errorf("List(prefix=sub) returned %q outside prefix", got)
		}
	}
}

func TestList_EmptyPrefixOnEmptyAdapter(t *testing.T) {
	a, _ := newAdapter(t)
	ch, err := a.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var count int
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("expected empty list on fresh adapter, got %d entries", count)
	}
}

func TestList_NonExistentPrefix(t *testing.T) {
	a, _ := newAdapter(t)
	ch, err := a.List(context.Background(), "no-such-prefix")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var count int
	for entry := range ch {
		if entry.Err != nil {
			t.Errorf("unexpected error on missing prefix: %v", entry.Err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("expected no entries for missing prefix, got %d", count)
	}
}

func TestReadStream_ByteRange(t *testing.T) {
	a, _ := newAdapter(t)
	body := []byte("the quick brown fox jumps over the lazy dog")
	if err := a.Write(context.Background(), "fox.txt", body); err != nil {
		t.Fatalf("Write: %v", err)
	}

	cases := []struct {
		offset, length int64
		want           string
	}{
		{0, 0, string(body)},
		{0, 9, "the quick"},
		{4, 5, "quick"},
		{int64(len(body)), 0, ""},
	}
	for _, tc := range cases {
		r, err := a.ReadStream(context.Background(), "fox.txt", tc.offset, tc.length)
		if err != nil {
			t.Fatalf("ReadStream(%d,%d): %v", tc.offset, tc.length, err)
		}
		got, _ := io.ReadAll(r)
		_ = r.Close()
		if string(got) != tc.want {
			t.Errorf("offset=%d length=%d: got %q, want %q", tc.offset, tc.length, got, tc.want)
		}
	}
}

func TestWriteStream_ReadStreamRoundTrip(t *testing.T) {
	a, _ := newAdapter(t)
	body := []byte("streamed content")
	if err := a.WriteStream(context.Background(), "s.bin", bytes.NewReader(body)); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}
	r, err := a.ReadStream(context.Background(), "s.bin", 0, 0)
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if !bytes.Equal(got, body) {
		t.Errorf("round-trip: got %q, want %q", got, body)
	}
}

func TestSignedURL_ReturnsUnsupported(t *testing.T) {
	a, _ := newAdapter(t)
	_, err := a.SignedURL(context.Background(), "anything", 0, storage.OpRead)
	if !errors.Is(err, storage.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported, got: %v", err)
	}
}

func TestHealthCheck_GoodAndBad(t *testing.T) {
	a, root := newAdapter(t)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on healthy adapter: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("removing root: %v", err)
	}
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Error("HealthCheck should fail when root is gone")
	}
}

func TestUninitializedAdapter_AllOperationsReturnError(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()
	ops := []struct {
		name string
		fn   func() error
	}{
		{"Read", func() error { _, err := a.Read(ctx, "p"); return err }},
		{"Write", func() error { return a.Write(ctx, "p", []byte("x")) }},
		{"Delete", func() error { return a.Delete(ctx, "p") }},
		{"Exists", func() error { _, err := a.Exists(ctx, "p"); return err }},
		{"Metadata", func() error { _, err := a.Metadata(ctx, "p"); return err }},
		{"HealthCheck", func() error { return a.HealthCheck(ctx) }},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			if err := op.fn(); err == nil {
				t.Errorf("%s should fail on uninitialized adapter", op.name)
			}
		})
	}
}

func TestConcurrent_ReadsAndWrites(t *testing.T) {
	a, _ := newAdapter(t)
	const goroutines = 8
	const iterations = 25

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				p := filepath.ToSlash(filepath.Join("concurrent", string(rune('a'+i%26)), "obj.bin"))
				body := []byte("body-" + string(rune('0'+j%10)))
				if err := a.Write(context.Background(), p, body); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
				if _, err := a.Read(context.Background(), p); err != nil {
					t.Errorf("Read: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Compile-time check that *Adapter satisfies the storage.Adapter
// interface, even before init() registers it. If the interface drifts,
// this build breaks loudly.
var _ storage.Adapter = (*Adapter)(nil)
