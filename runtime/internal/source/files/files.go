// Package files implements the source:files connector — a generic
// filesystem source that ingests markdown / plain-text files from a
// directory the user picks. No OAuth, no external service, no
// network. Useful for personal notes, journals, RFCs, captured
// research, and anything else that lives as text on disk.
//
// Files with YAML frontmatter:
//
//	---
//	from:    Sarah Smith <sarah@example.com>
//	to:      Bob Lee <bob@example.com>
//	subject: Project Alpha kickoff
//	thread:  proj-alpha-001
//	date:    2026-05-20T10:00:00Z
//	---
//
//	(body content here)
//
// …get rich metadata that the memory layer's entity + thread
// resolvers pick up automatically. Files without frontmatter still
// get ingested — they just won't show up under entities.list /
// threads.list (they'll still show up under memory.show / memory.query).
//
// Beyond Gmail's lessons: this is a deliberately boring source. No
// external auth, no rate limits, no API quotas. If something is
// wrong, it's wrong here in the source code, not at the network
// boundary. This makes it the natural test vector for end-to-end
// substrate verification.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

// SourceID is the registry id this connector registers under.
const SourceID = "source:files"

// Default extensions ingested when the user doesn't override.
var defaultExtensions = []string{".md", ".markdown", ".txt"}

// DefaultMaxFiles caps the first-sync file count. Same rationale as
// gmail's DefaultMaxFullSync — a directory with a million files
// shouldn't sync hot on first run.
const DefaultMaxFiles = 5000

// filesSource is the source:files adapter. One instance per
// configured source (e.g., "notes", "research-journal").
type filesSource struct {
	mu sync.Mutex

	deps source.Deps

	// Resolved config — immutable after Init.
	root       string
	extensions []string
	recursive  bool
	maxFiles   int
}

// New returns an uninitialized files source. Registered via init();
// callers should normally go through source.New("source:files").
func New() source.Source { return &filesSource{} }

func init() {
	source.Register(SourceID, New)
}

// ID implements source.Source.
func (f *filesSource) ID() string { return SourceID }

// Init implements source.Source.
//
// Required config:
//
//	root         — absolute path to the directory to ingest
//
// Optional config:
//
//	extensions   — []string of extensions to include (default: .md, .markdown, .txt)
//	recursive    — bool, walk subdirectories (default: true)
//	max_files    — int, cap on per-sync file count (default: 5000)
func (f *filesSource) Init(_ context.Context, deps source.Deps) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if deps.Storage == nil {
		return errors.New("source:files: no Storage adapter provided")
	}

	root, _ := deps.Config["root"].(string)
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("source:files: 'root' (directory path) is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("source:files: resolving root %q: %w", root, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("source:files: root %q is unreachable: %w", abs, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("source:files: root %q is not a directory", abs)
	}
	f.root = abs

	// Extensions — accept either []any (from YAML), []string, or comma-
	// separated string. Default to .md / .markdown / .txt.
	f.extensions = defaultExtensions
	switch v := deps.Config["extensions"].(type) {
	case []string:
		if len(v) > 0 {
			f.extensions = normalizeExtensions(v)
		}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			f.extensions = normalizeExtensions(out)
		}
	case string:
		if v != "" {
			parts := strings.Split(v, ",")
			f.extensions = normalizeExtensions(parts)
		}
	}

	// Recursive — default true.
	f.recursive = true
	if v, ok := deps.Config["recursive"].(bool); ok {
		f.recursive = v
	}

	// Max files — default DefaultMaxFiles.
	switch v := deps.Config["max_files"].(type) {
	case int:
		f.maxFiles = v
	case int64:
		f.maxFiles = int(v)
	case float64:
		f.maxFiles = int(v)
	default:
		f.maxFiles = DefaultMaxFiles
	}
	if f.maxFiles <= 0 {
		f.maxFiles = DefaultMaxFiles
	}

	f.deps = deps
	return nil
}

// AuthStatus implements source.Source. Filesystem access needs no
// authentication — as long as the runtime user can read the root,
// the source is "authenticated."
func (f *filesSource) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root == "" {
		return source.AuthStatus{Authenticated: false, Reason: "not initialized"}, nil
	}
	if _, err := os.Stat(f.root); err != nil {
		return source.AuthStatus{
			Authenticated: false,
			Reason:        fmt.Sprintf("root unreachable: %v", err),
		}, nil
	}
	return source.AuthStatus{Authenticated: true}, nil
}

// BeginAuth implements source.Source. No interactive auth needed —
// returns AuthFlowNone, signaling to the runtime that CompleteAuth
// will just validate the static config.
func (f *filesSource) BeginAuth(_ context.Context) (source.AuthFlow, error) {
	return source.AuthFlow{Kind: source.AuthFlowNone}, nil
}

// CompleteAuth implements source.Source. The runtime calls this
// after BeginAuth; for source:files we just re-stat the root and
// confirm it's still reachable. No params are read.
func (f *filesSource) CompleteAuth(ctx context.Context, _ map[string]string) error {
	status, err := f.AuthStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Authenticated {
		return errors.New("source:files: " + status.Reason)
	}
	return nil
}

// Sync implements source.Source. Walks the directory, ingests files
// matching the configured extensions, and tracks per-file content
// hashes in the cursor so subsequent syncs only re-ingest changed
// files.
func (f *filesSource) Sync(ctx context.Context, cursor []byte) (source.SyncResult, error) {
	started := time.Now().UTC()
	result := source.SyncResult{Started: started}

	state, err := decodeCursor(cursor)
	if err != nil {
		result.Finished = time.Now().UTC()
		return result, fmt.Errorf("decoding cursor: %w", err)
	}

	f.mu.Lock()
	root := f.root
	extensions := append([]string(nil), f.extensions...)
	recursive := f.recursive
	maxFiles := f.maxFiles
	memory := f.deps.Memory
	storage := f.deps.Storage
	sourceName := f.deps.SourceName
	f.mu.Unlock()

	// New cursor state. Files that disappear from the walk are
	// deleted at the end.
	newSeen := make(map[string]string, len(state.Hashes))

	scanned := 0
	walked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Errors = append(result.Errors, source.SyncError{
				RecordID: path,
				Reason:   walkErr.Error(),
			})
			return nil
		}
		if d.IsDir() {
			if !recursive && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		walked++
		if !matchesExtension(path, extensions) {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		scanned++

		// Context cancellation: bail out cleanly if the runtime
		// signals shutdown mid-walk.
		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			result.Errors = append(result.Errors, source.SyncError{
				RecordID: path,
				Reason:   err.Error(),
			})
			return nil
		}
		entryID := normalizeEntryID(rel)

		raw, err := os.ReadFile(path)
		if err != nil {
			result.Errors = append(result.Errors, source.SyncError{
				RecordID: entryID,
				Reason:   "read: " + err.Error(),
			})
			return nil
		}
		hash := contentHash(raw)
		newSeen[entryID] = hash

		// If we've seen this file with the same content before, skip.
		if prev, ok := state.Hashes[entryID]; ok && prev == hash {
			return nil
		}

		// Parse + ingest.
		st, statErr := d.Info()
		var mtime time.Time
		if statErr == nil {
			mtime = st.ModTime().UTC()
		}
		parsed := parseFile(raw)
		metadata := buildMetadata(rel, parsed, mtime)
		entryContent := parsed.body
		if entryContent == "" {
			// No frontmatter; use the whole file as content.
			entryContent = string(raw)
		}
		// Stash a bounded content snippet in metadata so MCP consumers
		// (memory.query, agents) can show users the gist of a hit
		// without a second round-trip to storage. Cap at 2 KiB — long
		// enough for a useful preview, short enough to keep the
		// metadata blob in the kilobyte range. The full body lives in
		// storage (storage.read) for callers that need it all.
		metadata["snippet"] = truncateForSnippet(entryContent, 2048)

		// Write the raw file to storage for the walkaway promise:
		// sources/<source_name>/files/<path>.
		storagePath := "sources/" + sourceName + "/files/" + rel
		if err := storage.Write(ctx, storagePath, raw); err != nil {
			result.Errors = append(result.Errors, source.SyncError{
				RecordID: entryID,
				Reason:   "storage.write: " + err.Error(),
			})
			return nil
		}
		result.BytesIngested += int64(len(raw))

		// Push to memory if a layer/adapter was wired. No-op
		// otherwise — the storage write is still useful on its own.
		if memory != nil {
			if err := memory.Upsert(ctx, source.MemoryEntry{
				Namespace: sourceName,
				ID:        entryID,
				Content:   entryContent,
				Metadata:  metadata,
			}); err != nil {
				result.Errors = append(result.Errors, source.SyncError{
					RecordID: entryID,
					Reason:   "memory.upsert: " + err.Error(),
				})
				return nil
			}
		}
		// Update or add: track which by checking prior hash existence.
		if _, ok := state.Hashes[entryID]; ok {
			result.RecordsUpdated++
		} else {
			result.RecordsAdded++
		}
		return nil
	})
	if err != nil {
		result.Finished = time.Now().UTC()
		return result, fmt.Errorf("walking %s: %w", root, err)
	}

	// Detect deletes: anything in the old cursor we didn't see this
	// pass. Remove from storage + memory layer.
	for id := range state.Hashes {
		if _, ok := newSeen[id]; ok {
			continue
		}
		// File disappeared. Best-effort cleanup.
		_ = storage.Delete(ctx, "sources/"+sourceName+"/files/"+denormalizeEntryID(id))
		if memory != nil {
			_ = memory.Delete(ctx, sourceName, id)
		}
	}

	result.Cursor = mustEncodeCursor(cursorPayload{
		Hashes:       newSeen,
		LastSyncTime: time.Now().UTC().Format(time.RFC3339Nano),
		Walked:       walked,
	})
	result.Finished = time.Now().UTC()
	return result, nil
}

// HealthCheck implements source.Source.
func (f *filesSource) HealthCheck(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root == "" {
		return nil
	}
	_, err := os.Stat(f.root)
	return err
}

// Close implements source.Source. Nothing to release — the source
// holds no long-lived resources beyond config.
func (f *filesSource) Close(_ context.Context) error { return nil }

// --- cursor encoding -------------------------------------------------

type cursorPayload struct {
	// Hashes maps entryID → content hash. We compare hashes on each
	// sync to detect changes; entries missing from the new walk are
	// treated as deletes.
	Hashes       map[string]string `json:"hashes"`
	LastSyncTime string            `json:"last_sync_time,omitempty"`
	Walked       int               `json:"walked,omitempty"`
}

func decodeCursor(b []byte) (cursorPayload, error) {
	if len(b) == 0 {
		return cursorPayload{Hashes: map[string]string{}}, nil
	}
	var c cursorPayload
	if err := json.Unmarshal(b, &c); err != nil {
		return cursorPayload{}, err
	}
	if c.Hashes == nil {
		c.Hashes = map[string]string{}
	}
	return c, nil
}

func mustEncodeCursor(c cursorPayload) []byte {
	b, _ := json.Marshal(c)
	return b
}

// --- helpers ---------------------------------------------------------

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8]) // truncated; collision risk negligible for personal use
}

func normalizeExtensions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, ext := range in {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		out = append(out, strings.ToLower(ext))
	}
	return out
}

func matchesExtension(path string, extensions []string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, want := range extensions {
		if ext == want {
			return true
		}
	}
	return false
}

// normalizeEntryID converts an OS path to a stable entry id.
// Uses forward slashes regardless of platform so the same corpus
// produces the same ids on Windows + Unix.
func normalizeEntryID(rel string) string {
	return filepath.ToSlash(rel)
}

func denormalizeEntryID(id string) string {
	return filepath.FromSlash(id)
}

func buildMetadata(rel string, parsed parsedFile, mtime time.Time) map[string]any {
	m := map[string]any{
		"path":        rel,
		"filename":    filepath.Base(rel),
		"extension":   strings.ToLower(filepath.Ext(rel)),
		"source_kind": "file",
	}
	if !mtime.IsZero() {
		m["mtime"] = mtime.Format(time.RFC3339)
	}
	// Frontmatter fields are mapped to the same metadata keys the
	// memory layer's resolver already reads. This is intentional —
	// the resolver doesn't need to know which source produced an
	// entry; "from" means "from" regardless.
	if parsed.from != "" {
		m["from"] = parsed.from
	}
	if parsed.to != "" {
		m["to"] = parsed.to
	}
	if parsed.cc != "" {
		m["cc"] = parsed.cc
	}
	if parsed.bcc != "" {
		m["bcc"] = parsed.bcc
	}
	if parsed.subject != "" {
		m["subject"] = parsed.subject
	} else if parsed.title != "" {
		m["subject"] = parsed.title
	}
	if parsed.thread != "" {
		// The memory layer's thread resolver reads gmail_thread_id;
		// reuse that key so the existing resolver fires unchanged.
		// When we generalize the resolver to a "thread_id" key, this
		// becomes the migration point.
		m["gmail_thread_id"] = parsed.thread
	}
	if !parsed.date.IsZero() {
		m["internal_date"] = parsed.date.Format(time.RFC3339)
	}
	// Pass through any unknown frontmatter keys so capsules can read
	// them — but namespace them under "frontmatter" so they don't
	// collide with the resolver's expected keys.
	if len(parsed.extras) > 0 {
		m["frontmatter"] = parsed.extras
	}
	return m
}

// truncateForSnippet returns the first max bytes of s, trimmed at a
// rune boundary, with an ellipsis appended when truncation happened.
// Used to bound the metadata.snippet field so the memory adapter's
// metadata blob stays small.
func truncateForSnippet(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Walk back to the nearest rune boundary so we don't slice a
	// multi-byte character in half.
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
