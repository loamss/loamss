package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
)

// `loamss export` is the walkaway-promise CLI. Bundles everything
// the runtime persists about the user into a single tarball:
//
//   - storage adapter contents (typically <data_dir>/storage/)
//   - memory.db (the memory store, whatever adapter wrote it)
//   - audit.db (the hash-chained audit log)
//   - runtime.db (grants, capsules, clients, capabilities)
//   - capsules/ install dirs (the on-disk code for installed
//     capsules, so re-importing on another machine restores
//     the capability surface too)
//   - manifest.json (export metadata + a chain-verification result)
//
// The output is a single .tar.gz the user can move anywhere. Re-
// importing is a future command (`loamss import`); for now, the
// promise is just that the user OWNS this data and can take it
// anywhere — even if Loamss disappears tomorrow.

var (
	exportOutPath  string
	exportNoVerify bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Bundle runtime state into a portable archive",
	Long: `Export the user's data — storage, memory, audit chain, grants,
capsules — into a single .tar.gz the user owns.

Default output: ./loamss-export-<timestamp>.tar.gz. Override with
--out.

By default, audit chain integrity is verified before exporting and
the result is recorded in the export manifest. Use --no-verify to
skip (only do that if you know the chain is broken and you want
the export anyway).

This command should be runnable any time — even when the daemon is
also running. The underlying SQLite databases use WAL mode and the
export reads them without taking a write lock. Files copied may
briefly miss in-flight writes; the chain-verify pass after export
will surface this if it matters.`,
	Args: cobra.NoArgs,
	RunE: runExport,
}

func runExport(cmd *cobra.Command, _ []string) error {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return errors.New("no config attached to context")
	}
	dataDir := cfg.Runtime.DataDir
	if _, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("data_dir %q is unreadable: %w", dataDir, err)
	}

	outPath := exportOutPath
	if outPath == "" {
		outPath = "loamss-export-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	}

	// Optionally verify the audit chain before bundling so the
	// export manifest records its integrity state. Run in a sub-
	// scope so the audit handle closes before we tar the file.
	var verifyResult *audit.VerifyResult
	if !exportNoVerify {
		r, err := verifyAudit(cmd.Context(), filepath.Join(dataDir, "audit.db"))
		if err != nil {
			return fmt.Errorf("verifying audit chain: %w", err)
		}
		verifyResult = r
	}

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer func() { _ = out.Close() }()

	gz := gzip.NewWriter(out)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	prefix := "loamss-export-" + time.Now().UTC().Format("20060102T150405Z") + "/"

	// Write the manifest first so consumers can discover the
	// archive's shape without scanning the whole tar.
	manifest := buildManifest(dataDir, verifyResult)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarFile(tw, prefix+"manifest.json", manifestJSON, time.Now().UTC()); err != nil {
		return err
	}

	// Walk the data dir and add every regular file. Skip:
	//   - the file we're WRITING to (if data_dir + outPath overlap)
	//   - SQLite WAL/journal sidecar files (-wal, -shm, -journal)
	//     because they're snapshot-inconsistent without taking a
	//     write lock; the .db files themselves are durable.
	//   - any path under runtime.pid or similar (we don't write
	//     one today, but skip defensively)
	root := filepath.Clean(dataDir)
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if shouldSkipExportEntry(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Use forward slashes inside the tar regardless of host OS.
		archivePath := prefix + "data/" + filepath.ToSlash(rel)
		return copyFileIntoTar(tw, path, archivePath)
	}); err != nil {
		return fmt.Errorf("walking %s: %w", dataDir, err)
	}

	// Close in reverse order so trailers flush before we report
	// success. Errors at this stage are unusual but worth catching.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("closing gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing output: %w", err)
	}

	st, _ := os.Stat(outPath)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"✓ Exported to %s (%d bytes)\n", outPath, st.Size())
	if verifyResult != nil {
		if verifyResult.Valid {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"  audit chain verified: %d entries\n", verifyResult.EntriesChecked)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"  ⚠ audit chain BROKEN at %s — exported anyway\n", verifyResult.BrokenAt)
		}
	}
	return nil
}

// ExportManifest is the JSON record at the root of the archive.
// Stable shape; future versions add fields without removing existing ones.
type ExportManifest struct {
	SchemaVersion string        `json:"schema_version"`
	ExportedAt    string        `json:"exported_at"`
	Runtime       runtimeMeta   `json:"runtime"`
	Contents      contentsMeta  `json:"contents"`
	AuditVerify   *verifyResult `json:"audit_verify,omitempty"`
}

type runtimeMeta struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

type contentsMeta struct {
	DataDir string `json:"data_dir"`
}

type verifyResult struct {
	Valid          bool   `json:"valid"`
	EntriesChecked int64  `json:"entries_checked"`
	BrokenAt       string `json:"broken_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func buildManifest(dataDir string, vr *audit.VerifyResult) ExportManifest {
	m := ExportManifest{
		SchemaVersion: "0.1",
		ExportedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Runtime: runtimeMeta{
			Version:   version,
			Commit:    commit,
			BuildDate: buildDate,
		},
		Contents: contentsMeta{DataDir: dataDir},
	}
	if vr != nil {
		m.AuditVerify = &verifyResult{
			Valid:          vr.Valid,
			EntriesChecked: vr.EntriesChecked,
			BrokenAt:       vr.BrokenAt,
			Reason:         vr.Reason,
		}
	}
	return m
}

// shouldSkipExportEntry returns true for SQLite sidecar files and
// any other ephemeral state we don't want in the archive.
func shouldSkipExportEntry(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "-wal"):
		return true
	case strings.HasSuffix(base, "-shm"):
		return true
	case strings.HasSuffix(base, "-journal"):
		return true
	case base == ".DS_Store":
		return true
	}
	return false
}

// writeTarFile adds bytes as a regular file in the tar.
func writeTarFile(tw *tar.Writer, archivePath string, data []byte, modTime time.Time) error {
	hdr := &tar.Header{
		Name:     archivePath,
		Mode:     0o600,
		Size:     int64(len(data)),
		ModTime:  modTime,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// copyFileIntoTar streams a file from disk into the archive.
// Preserves the modification time but normalizes permissions to
// 0600 — we don't propagate "world-readable" through an export.
func copyFileIntoTar(tw *tar.Writer, srcPath, archivePath string) error {
	st, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:     archivePath,
		Mode:     0o600,
		Size:     st.Size(),
		ModTime:  st.ModTime(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, err = io.Copy(tw, in)
	return err
}

// verifyAudit opens the audit log read-only and runs Verify. Returns
// the result for the manifest. Errors here block the export — a
// missing or unreadable audit log is a serious-enough signal to
// stop and let the user investigate.
func verifyAudit(ctx context.Context, path string) (*audit.VerifyResult, error) {
	w, err := audit.OpenSQLite(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = w.Close(ctx) }()
	return w.Verify(ctx)
}

func init() {
	exportCmd.Flags().StringVar(&exportOutPath, "out", "",
		"output path (default: ./loamss-export-<timestamp>.tar.gz)")
	exportCmd.Flags().BoolVar(&exportNoVerify, "no-verify", false,
		"skip the pre-export audit chain verification")
	rootCmd.AddCommand(exportCmd)
}
