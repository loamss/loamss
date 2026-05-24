// Package fsencrypted implements the storage:fs-encrypted adapter — a
// local filesystem backend that encrypts every object at rest using
// AES-256-GCM with a single per-adapter master key.
//
// On-disk file layout (one file per object):
//
//	[1 byte version=1][12 byte nonce][ciphertext + 16 byte GCM tag]
//
// Each Write generates a fresh random nonce. Encryption is whole-object;
// streaming reads/writes load the whole object into memory in v0.1.
// Large-file streaming via chunked GCM is future work — not needed for
// the audit log, memory db, or per-thread email JSON we use first.
//
// The master key is stored at <root>/.loamss-meta/master.key (32 bytes,
// chmod 0600). If absent at Init time, it is generated. The user is
// responsible for backing up this file; without it, the encrypted
// objects are unrecoverable.
package fsencrypted

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// adapterID is the canonical id under which this adapter registers.
const adapterID = "storage:fs-encrypted"

func init() {
	storage.Register(adapterID, func() storage.Adapter { return &Adapter{} })
}

// Wire format constants.
const (
	formatVersion   byte = 1
	versionHeaderSz      = 1
	nonceSz              = 12 // GCM standard
	gcmTagSz             = 16
	headerOverhead       = versionHeaderSz + nonceSz // bytes before ciphertext
	objectOverhead       = headerOverhead + gcmTagSz // total non-content bytes per file
)

// Subdirectory under the storage root reserved for adapter metadata
// (the master key, and any future internal state). Excluded from List.
const metaDir = ".loamss-meta"
const keyFile = "master.key"
const keySize = 32 // AES-256

// Adapter is the fs-encrypted concrete storage adapter.
//
// Zero value is unusable; call Init before any other method. After
// Init, all methods are safe for concurrent use.
type Adapter struct {
	mu     sync.RWMutex
	root   string
	gcm    cipher.AEAD
	inited bool
}

// Init reads config, ensures the root exists, and loads-or-generates
// the master key. Expected config:
//
//	root: <path>     # required; where encrypted objects live
//
// Future config fields (deferred): key_path (override the default
// <root>/.loamss-meta/master.key location).
func (a *Adapter) Init(_ context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	rootRaw, ok := config["root"]
	if !ok {
		return errors.New("fs-encrypted: missing required config: root")
	}
	root, ok := rootRaw.(string)
	if !ok || root == "" {
		return fmt.Errorf("fs-encrypted: root must be a non-empty string (got %T %v)", rootRaw, rootRaw)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("fs-encrypted: resolving root %q: %w", root, err)
	}

	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("fs-encrypted: creating root %q: %w", abs, err)
	}
	if err := os.MkdirAll(filepath.Join(abs, metaDir), 0o700); err != nil {
		return fmt.Errorf("fs-encrypted: creating meta dir: %w", err)
	}

	key, err := loadOrCreateKey(filepath.Join(abs, metaDir, keyFile))
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("fs-encrypted: building cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("fs-encrypted: building GCM: %w", err)
	}

	a.root = abs
	a.gcm = gcm
	a.inited = true
	return nil
}

// loadOrCreateKey returns the 32-byte master key, generating it on
// disk if absent. The key file is written with 0600 permissions; its
// parent should already be 0700 from Init's mkdir.
func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != keySize {
			return nil, fmt.Errorf("fs-encrypted: key file %s has wrong size: got %d, want %d", path, len(data), keySize)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("fs-encrypted: reading key file %s: %w", path, err)
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("fs-encrypted: generating key: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("fs-encrypted: writing key file %s: %w", path, err)
	}
	return key, nil
}

// --- Read paths --------------------------------------------------------

// Read decrypts and returns the entire object at path.
func (a *Adapter) Read(_ context.Context, p string) ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return nil, err
	}
	abs, err := a.resolve(p)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, p)
		}
		return nil, fmt.Errorf("fs-encrypted: reading %s: %w", abs, err)
	}
	return a.decryptObject(data, p)
}

// ReadStream returns the requested byte range as a ReadCloser. GCM
// requires the whole object to be authenticated before any bytes are
// trusted, so v0.1 decrypts the whole object and returns a reader
// over the requested slice. Acceptable for our current size profile;
// see package doc for the future improvement plan.
//
// length == 0 means "to the end of the object".
func (a *Adapter) ReadStream(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	plain, err := a.Read(ctx, p)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, fmt.Errorf("fs-encrypted: negative offset %d", offset)
	}
	if int64(len(plain)) < offset {
		return io.NopCloser(strings.NewReader("")), nil
	}
	tail := plain[offset:]
	if length > 0 && int64(len(tail)) > length {
		tail = tail[:length]
	}
	return io.NopCloser(strings.NewReader(string(tail))), nil
}

// decryptObject parses the header and uses the AEAD to authenticate
// and decrypt. Returns the plaintext on success.
func (a *Adapter) decryptObject(data []byte, p string) ([]byte, error) {
	if len(data) < headerOverhead+gcmTagSz {
		return nil, fmt.Errorf("fs-encrypted: object %s is too short (%d bytes)", p, len(data))
	}
	if data[0] != formatVersion {
		return nil, fmt.Errorf("fs-encrypted: object %s has unsupported format version %d", p, data[0])
	}
	nonce := data[versionHeaderSz : versionHeaderSz+nonceSz]
	ct := data[versionHeaderSz+nonceSz:]
	plain, err := a.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("fs-encrypted: decrypting %s: %w", p, err)
	}
	return plain, nil
}

// --- Write paths -------------------------------------------------------

// Write encrypts content and stores it at path. Atomic via temp file
// + rename in the destination's directory (no cross-fs renames).
func (a *Adapter) Write(_ context.Context, p string, content []byte) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return err
	}
	abs, err := a.resolve(p)
	if err != nil {
		return err
	}

	encoded, err := a.encryptObject(content)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("fs-encrypted: creating parent for %s: %w", abs, err)
	}

	return atomicWriteFile(abs, encoded, 0o600)
}

// WriteStream reads everything from content and stores it. Loads into
// memory; see Adapter package doc for the streaming caveat.
func (a *Adapter) WriteStream(ctx context.Context, p string, content io.Reader) error {
	buf, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("fs-encrypted: reading content stream: %w", err)
	}
	return a.Write(ctx, p, buf)
}

func (a *Adapter) encryptObject(plain []byte) ([]byte, error) {
	nonce := make([]byte, nonceSz)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("fs-encrypted: generating nonce: %w", err)
	}
	out := make([]byte, 0, headerOverhead+len(plain)+gcmTagSz)
	out = append(out, formatVersion)
	out = append(out, nonce...)
	out = a.gcm.Seal(out, nonce, plain, nil)
	return out, nil
}

func atomicWriteFile(target string, data []byte, perm os.FileMode) (retErr error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing temp file %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync temp file %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temp file %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, target, err)
	}
	return nil
}

// --- Delete / Exists / Metadata ----------------------------------------

// Delete is idempotent: nil if the object already doesn't exist.
func (a *Adapter) Delete(_ context.Context, p string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return err
	}
	abs, err := a.resolve(p)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs-encrypted: removing %s: %w", abs, err)
	}
	return nil
}

// Exists reports whether an object lives at p. Returns false for
// missing files or for directory entries (we never store objects as
// directories).
func (a *Adapter) Exists(_ context.Context, p string) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return false, err
	}
	abs, err := a.resolve(p)
	if err != nil {
		return false, err
	}
	stat, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("fs-encrypted: stat %s: %w", abs, err)
	}
	if stat.IsDir() {
		// We never write directories as objects; treat as not-found.
		return false, nil
	}
	return true, nil
}

// Metadata returns size (content size, not encrypted-file size),
// mtime, and a best-effort content type for the object at p.
func (a *Adapter) Metadata(_ context.Context, p string) (storage.ObjectMetadata, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return storage.ObjectMetadata{}, err
	}
	abs, err := a.resolve(p)
	if err != nil {
		return storage.ObjectMetadata{}, err
	}
	stat, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.ObjectMetadata{}, fmt.Errorf("%w: %s", storage.ErrNotFound, p)
		}
		return storage.ObjectMetadata{}, fmt.Errorf("fs-encrypted: stat %s: %w", abs, err)
	}
	if stat.IsDir() {
		return storage.ObjectMetadata{}, fmt.Errorf("%w: %s (is a directory)", storage.ErrNotFound, p)
	}
	contentSize := stat.Size() - int64(objectOverhead)
	if contentSize < 0 {
		contentSize = 0
	}
	return storage.ObjectMetadata{
		Path:        p,
		Size:        contentSize,
		ModTime:     stat.ModTime(),
		ContentType: mime.TypeByExtension(strings.ToLower(filepath.Ext(p))),
	}, nil
}

// --- List --------------------------------------------------------------

// List walks the storage tree under prefix and emits entries on the
// returned channel. The walk skips the .loamss-meta/ subtree so the
// master key is never exposed.
//
// On error during walk, a final ListEntry with Err set is emitted,
// then the channel is closed. Callers must drain the channel.
func (a *Adapter) List(ctx context.Context, prefix string) (<-chan storage.ListEntry, error) {
	a.mu.RLock()
	if err := a.assertInited(); err != nil {
		a.mu.RUnlock()
		return nil, err
	}
	root := a.root
	a.mu.RUnlock()

	cleanPrefix, err := cleanInputPath(prefix, true)
	if err != nil {
		return nil, err
	}
	startAbs := filepath.Join(root, cleanPrefix)

	out := make(chan storage.ListEntry, 16)
	go func() {
		defer close(out)
		err := filepath.WalkDir(startAbs, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) && p == startAbs {
					// Empty result on non-existent prefix.
					return nil
				}
				return walkErr
			}
			// Skip the meta subtree wherever it appears.
			if d.IsDir() {
				if d.Name() == metaDir {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			contentSize := info.Size() - int64(objectOverhead)
			if contentSize < 0 {
				contentSize = 0
			}
			entry := storage.ListEntry{
				Metadata: storage.ObjectMetadata{
					Path:        filepath.ToSlash(rel),
					Size:        contentSize,
					ModTime:     info.ModTime(),
					ContentType: mime.TypeByExtension(strings.ToLower(filepath.Ext(rel))),
				},
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- entry:
				return nil
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case out <- storage.ListEntry{Err: fmt.Errorf("fs-encrypted: walking %s: %w", startAbs, err)}:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

// --- Other -------------------------------------------------------------

// SignedURL is not supported by a plain filesystem backend.
func (a *Adapter) SignedURL(context.Context, string, time.Duration, storage.Op) (string, error) {
	return "", storage.ErrUnsupported
}

// HealthCheck verifies the root is still reachable as a directory.
func (a *Adapter) HealthCheck(_ context.Context) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.assertInited(); err != nil {
		return err
	}
	stat, err := os.Stat(a.root)
	if err != nil {
		return fmt.Errorf("fs-encrypted: stat root %s: %w", a.root, err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("fs-encrypted: root %s is not a directory", a.root)
	}
	return nil
}

// Close releases adapter resources. Currently a no-op; future
// versions may want to scrub the in-memory key.
func (a *Adapter) Close(_ context.Context) error { return nil }

// --- Helpers -----------------------------------------------------------

func (a *Adapter) assertInited() error {
	if !a.inited {
		return errors.New("fs-encrypted: adapter used before Init")
	}
	return nil
}

// resolve validates the user-supplied path and joins it under the
// configured root, returning an absolute filesystem path. Rejects
// any path that would escape root.
func (a *Adapter) resolve(userPath string) (string, error) {
	clean, err := cleanInputPath(userPath, false)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return "", errors.New("fs-encrypted: empty path")
	}
	if strings.HasPrefix(clean, metaDir+"/") || clean == metaDir {
		return "", fmt.Errorf("fs-encrypted: path %q is reserved", userPath)
	}
	abs := filepath.Join(a.root, clean)
	// Defense in depth: confirm the joined path is still under root.
	rel, err := filepath.Rel(a.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("fs-encrypted: path %q escapes root", userPath)
	}
	return abs, nil
}

// cleanInputPath validates and normalizes a user-supplied path.
// allowEmpty controls whether "" is accepted (true for List prefixes).
func cleanInputPath(p string, allowEmpty bool) (string, error) {
	if p == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("fs-encrypted: empty path")
	}
	if strings.Contains(p, "\x00") {
		return "", errors.New("fs-encrypted: null byte in path")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("fs-encrypted: absolute paths not allowed (%q)", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("fs-encrypted: path traversal not allowed (%q)", p)
	}
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("fs-encrypted: bare %q not allowed", p)
	}
	return clean, nil
}
