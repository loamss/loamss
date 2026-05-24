package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// `config.WriteAtomic` persists a Config to YAML at path. The write
// is atomic in the rename sense: the file appears either complete or
// not at all, even if the process is killed mid-write.
//
// Two failure modes the caller branches on:
//
//   ErrAlreadyExists — the destination file already exists. The
//                      caller decides whether to back it up + replace
//                      (WriteAtomic does not). Setting opts.Overwrite=true
//                      bypasses this check.
//
//   any other error  — disk full, permission denied, parent dir
//                      unwriteable, etc. Propagated verbatim.
//
// The file is written with mode 0600 (user read/write only) since
// it may contain API keys until those move to the OS keychain. The
// parent directory is created with mode 0700 if missing.

// ErrAlreadyExists signals a config file is already present at the
// destination path. The caller decides whether to overwrite.
var ErrAlreadyExists = errors.New("config: file already exists")

// WriteOptions controls the WriteAtomic behavior.
type WriteOptions struct {
	// Overwrite, when true, replaces an existing file without
	// erroring. When false (the default), a present file at the
	// destination causes ErrAlreadyExists.
	Overwrite bool

	// BackupSuffix, when non-empty AND Overwrite is true, renames
	// the existing file to path + suffix before writing the new
	// one. ".bak" is a common choice. Any "%s" token in the suffix
	// is replaced with a UTC timestamp (YYYYMMDD-HHMMSS); use
	// ".%s.bak" to get filenames like config.yaml.20260524-153000.bak
	// that don't clobber each other across re-runs. Empty suffix →
	// no backup.
	BackupSuffix string

	// Header is a YAML comment block emitted at the top of the file
	// before the config body. Useful for the "this file is managed
	// by the console wizard; edits are preserved on next write"
	// note we want users to see.
	Header string
}

// WriteAtomic persists cfg to path. See file-level doc for semantics.
func WriteAtomic(path string, cfg *Config, opts WriteOptions) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("config: resolving path %q: %w", path, err)
	}

	// Check for an existing file unless the caller opted into
	// overwriting. The check + write isn't atomic w.r.t. each other —
	// a concurrent writer could race in between — but the wizard is
	// single-tenant and the runtime isn't expected to write its own
	// config concurrently.
	if !opts.Overwrite {
		if _, err := os.Stat(abs); err == nil {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, abs)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config: stat %s: %w", abs, err)
		}
	}

	// Validate cfg before writing. Refuse to persist a config the
	// loader would reject — saves a class of "console said success
	// but loamss start fails" issues.
	if err := validate(cfg); err != nil {
		return fmt.Errorf("config: refusing to write invalid config: %w", err)
	}

	// Ensure parent dir exists. 0700 because the file may carry
	// secrets (api keys until they move to the OS keychain).
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: creating parent dir %s: %w", dir, err)
	}

	// If the user asked for a backup and a file already exists,
	// rename it aside before we write the new one.
	//
	// Rapid re-runs of the wizard can land two overwrites within the
	// same UTC second, which would compute the same backup filename
	// twice. Because os.Rename silently replaces an existing target
	// on POSIX, the naive implementation would lose the older
	// backup. We instead resolve a unique target by appending "-N"
	// when the computed path collides — preserving the user-visible
	// invariant that every overwrite leaves an accessible backup.
	if opts.Overwrite && opts.BackupSuffix != "" {
		if _, err := os.Stat(abs); err == nil {
			// Replace any "%s" token in the suffix with a UTC
			// timestamp. We use strings.Replace (not fmt.Sprintf) so
			// other %-verbs in the suffix don't surprise the caller.
			suffix := strings.ReplaceAll(
				opts.BackupSuffix,
				"%s",
				time.Now().UTC().Format("20060102-150405"),
			)
			backupPath := uniqueBackupPath(abs + suffix)
			if err := os.Rename(abs, backupPath); err != nil {
				return fmt.Errorf("config: backing up old file: %w", err)
			}
		}
	}

	// Serialize. Use yaml.v3 with two-space indent for readability.
	body, err := encodeYAML(cfg, opts.Header)
	if err != nil {
		return err
	}

	// Atomic write: write to a sibling temp file with the same
	// permissions, then rename. The rename is the atomic step on
	// every POSIX filesystem we target.
	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("config: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: writing temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, abs); err != nil {
		cleanup()
		return fmt.Errorf("config: renaming into place: %w", err)
	}
	return nil
}

// uniqueBackupPath returns `desired` if no file exists there, or
// the first available "<desired-without-final-ext>-<N><ext>" name
// otherwise. Used so two backups computed in the same UTC second
// don't clobber each other.
//
// Examples (when config.yaml.20260524-231631.bak already exists):
//
//	uniqueBackupPath("/d/config.yaml.20260524-231631.bak")
//	  → "/d/config.yaml.20260524-231631-2.bak"
//
// We loop until we find a free slot; the bound is high enough that
// realistic overwrite rates (clicks per second from a single user)
// can never hit it.
func uniqueBackupPath(desired string) string {
	if _, err := os.Stat(desired); errors.Is(err, os.ErrNotExist) {
		return desired
	}
	// Split into base + final extension so the disambiguator lands
	// between the timestamp and the trailing ".bak" (or whatever
	// extension the suffix ended with): config.yaml.TS-2.bak rather
	// than config.yaml.TS.bak-2.
	ext := filepath.Ext(desired)
	stem := strings.TrimSuffix(desired, ext)
	// Cap at 10_000 to avoid an unbounded loop in pathological
	// cases (e.g., the directory already has every disambiguator
	// pre-filled). 10k same-second overwrites of one config file is
	// well past anything a user could produce.
	for n := 2; n < 10_000; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	// Last-resort fallback: append a nanosecond suffix. Ugly but
	// guaranteed unique to within ~1ns. Reaching this branch
	// requires 10k pre-existing collisions which we treat as a
	// degenerate filesystem state worth surfacing in the filename.
	return fmt.Sprintf("%s-ns%d%s", stem, time.Now().UnixNano(), ext)
}

// DefaultPath returns where Load would look for a config file
// when no explicit path is supplied. Useful for the console
// wizard's "where will this be written?" preview.
func DefaultPath() string {
	dataDir := defaultDataDir()
	if v := os.Getenv(envDataDir); v != "" {
		dataDir = v
	}
	return filepath.Join(dataDir, "config.yaml")
}

// encodeYAML serializes cfg to YAML with the given header comment.
// Two-space indent for readability; explicit document end marker
// omitted (uncommon for hand-edited configs).
func encodeYAML(cfg *Config, header string) ([]byte, error) {
	var buf yamlBuffer
	if header != "" {
		// Each line of the header gets a "# " prefix. Single
		// trailing blank line after the block.
		for _, line := range splitLines(header) {
			if line == "" {
				_, _ = buf.WriteString("#\n")
				continue
			}
			_, _ = buf.WriteString("# ")
			_, _ = buf.WriteString(line)
			_, _ = buf.WriteString("\n")
		}
		_, _ = buf.WriteString("\n")
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("config: encoding yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: closing yaml encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// yamlBuffer is a tiny bytes.Buffer-shaped writer. We don't depend
// on bytes.Buffer directly because we already have a stdlib io
// import and want to keep the dependency surface explicit in this
// file (helps readers track what this writer touches).
type yamlBuffer struct {
	b []byte
}

func (y *yamlBuffer) Write(p []byte) (int, error) {
	y.b = append(y.b, p...)
	return len(p), nil
}

func (y *yamlBuffer) WriteString(s string) (int, error) {
	y.b = append(y.b, s...)
	return len(s), nil
}

func (y *yamlBuffer) Bytes() []byte { return y.b }

// Assert yamlBuffer implements io.Writer (yaml.NewEncoder needs one).
var _ io.Writer = (*yamlBuffer)(nil)

// splitLines splits s by '\n'. Used by encodeYAML for the header
// comment block; we avoid pulling in strings.Split just to keep the
// number of imports in this file small.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
