package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// Adapter is the narrow surface of the memory adapter the Layer uses.
// Lets tests substitute a fake adapter without depending on a full
// sqlite-vec setup.
type Adapter interface {
	Upsert(ctx context.Context, id string, vector []float32, metadata map[string]any) error
	Delete(ctx context.Context, id string) error
}

// Layer is the semantic memory surface above the vector adapter.
// Writes go through Upsert/Delete here; the Layer fans out to the
// adapter (vectors + raw metadata) and to its own store (entities,
// threads, mappings).
//
// Reads on derived state (ListEntities, GetThread, …) come straight
// from the store — they don't round-trip the adapter. Reads on
// entry content still go through the adapter; the Layer doesn't
// duplicate that data.
type Layer interface {
	// Upsert writes an entry to the adapter (vectors + metadata) and
	// extracts entities + threads into the layer's tables. The id
	// inside the adapter is `<namespace>:<entry.id>` so namespaces
	// don't collide.
	Upsert(ctx context.Context, entry Entry) error

	// Delete removes the entry from the adapter and cascade-cleans
	// the entity + thread mappings.
	Delete(ctx context.Context, namespace, id string) error

	// ListEntities returns entities matching the filter.
	ListEntities(ctx context.Context, filter EntityFilter) ([]Entity, error)

	// GetEntity returns one entity by id, or ErrEntityNotFound.
	GetEntity(ctx context.Context, id string) (*Entity, error)

	// EntriesByEntity returns entry references for an entity, newest-
	// first. Each ref includes the role the entity played in the entry.
	EntriesByEntity(ctx context.Context, entityID string, limit int) ([]EntryRef, error)

	// ListThreads returns threads matching the filter.
	ListThreads(ctx context.Context, filter ThreadFilter) ([]Thread, error)

	// GetThread returns one thread by id, or ErrThreadNotFound.
	GetThread(ctx context.Context, id string) (*Thread, error)

	// EntriesByThread returns entry references for a thread in
	// reading order (oldest-first).
	EntriesByThread(ctx context.Context, threadID string, limit int) ([]EntryRef, error)

	// Close releases the layer's resources. The wrapped adapter is
	// the caller's responsibility — Layer doesn't close it.
	Close() error
}

// New constructs a Layer that writes to the given adapter and stores
// derived state in store.
func New(adapter Adapter, store *Store, logger *slog.Logger) Layer {
	if logger == nil {
		logger = slog.Default()
	}
	return &layerImpl{
		adapter: adapter,
		store:   store,
		logger:  logger,
	}
}

// layerImpl is the concrete Layer. Stateless beyond its constructor
// arguments; safe for concurrent use (the store synchronizes its
// own writes).
type layerImpl struct {
	adapter Adapter
	store   *Store
	logger  *slog.Logger
}

// Upsert writes the entry to the adapter and updates derived state.
//
// Failure modes:
//   - Adapter write fails → return the error; nothing in the layer
//     was touched, so the failure is clean.
//   - Adapter succeeds but layer derivation fails → log a warning,
//     return success. The entry is searchable; entity / thread
//     extraction can be redone by re-Upserting later. We don't
//     "fail the whole upsert" because the user-visible memory entry
//     IS the adapter row; the layer is a secondary index.
func (l *layerImpl) Upsert(ctx context.Context, entry Entry) error {
	if entry.Namespace == "" || entry.ID == "" {
		return errors.New("memory layer: Upsert requires Namespace and ID")
	}

	// Adapter write first. The adapter id namespace-qualifies the
	// entry so different sources can use the same per-source id
	// without collision.
	adapterID := composeAdapterID(entry.Namespace, entry.ID)
	metadata := withNamespace(entry.Metadata, entry.Namespace, entry.ID)
	if err := l.adapter.Upsert(ctx, adapterID, entry.Embeddings, metadata); err != nil {
		return fmt.Errorf("memory layer: adapter upsert: %w", err)
	}

	// Derived state. Errors here are warnings — they don't fail the
	// caller, but the entry will be missing from entity/thread views
	// until re-upserted or until Rebuild lands.
	if err := l.deriveEntities(ctx, entry); err != nil {
		l.logger.Warn("memory layer: entity derivation failed",
			"namespace", entry.Namespace, "id", entry.ID, "err", err)
	}
	if err := l.deriveThread(ctx, entry); err != nil {
		l.logger.Warn("memory layer: thread derivation failed",
			"namespace", entry.Namespace, "id", entry.ID, "err", err)
	}
	return nil
}

// Delete removes the entry from the adapter and unlinks all mappings.
func (l *layerImpl) Delete(ctx context.Context, namespace, id string) error {
	if namespace == "" || id == "" {
		return errors.New("memory layer: Delete requires namespace and id")
	}
	if err := l.adapter.Delete(ctx, composeAdapterID(namespace, id)); err != nil {
		return fmt.Errorf("memory layer: adapter delete: %w", err)
	}
	if err := l.store.UnlinkEntry(ctx, namespace, id); err != nil {
		l.logger.Warn("memory layer: unlink on delete failed",
			"namespace", namespace, "id", id, "err", err)
	}
	return nil
}

// ListEntities is a thin pass-through to the store.
func (l *layerImpl) ListEntities(ctx context.Context, filter EntityFilter) ([]Entity, error) {
	return l.store.ListEntities(ctx, filter)
}

// GetEntity is a thin pass-through to the store.
func (l *layerImpl) GetEntity(ctx context.Context, id string) (*Entity, error) {
	return l.store.GetEntity(ctx, id)
}

// EntriesByEntity is a thin pass-through to the store.
func (l *layerImpl) EntriesByEntity(ctx context.Context, entityID string, limit int) ([]EntryRef, error) {
	return l.store.EntriesByEntity(ctx, entityID, limit)
}

// ListThreads is a thin pass-through to the store.
func (l *layerImpl) ListThreads(ctx context.Context, filter ThreadFilter) ([]Thread, error) {
	return l.store.ListThreads(ctx, filter)
}

// GetThread is a thin pass-through to the store.
func (l *layerImpl) GetThread(ctx context.Context, id string) (*Thread, error) {
	return l.store.GetThread(ctx, id)
}

// EntriesByThread is a thin pass-through to the store.
func (l *layerImpl) EntriesByThread(ctx context.Context, threadID string, limit int) ([]EntryRef, error) {
	return l.store.EntriesByThread(ctx, threadID, limit)
}

// Close releases the layer's own resources. Caller is responsible
// for closing the wrapped adapter.
func (l *layerImpl) Close() error {
	return l.store.Close()
}

// --- internals --------------------------------------------------------

func (l *layerImpl) deriveEntities(ctx context.Context, entry Entry) error {
	ext := ExtractEntities(entry)
	if len(ext.Entities) == 0 {
		return nil
	}
	for _, e := range ext.Entities {
		// Stamp first/last seen on the entity from the entry date
		// when available — gives the entity's time range some
		// signal even when the entries themselves arrive in any order.
		entityCopy := e.Entity
		if !ext.EntryDate.IsZero() {
			entityCopy.FirstSeen = ext.EntryDate
			entityCopy.LastSeen = ext.EntryDate
		}
		stored, err := l.store.UpsertEntity(ctx, entityCopy)
		if err != nil {
			return err
		}
		if err := l.store.LinkEntityEntry(ctx, stored.ID, entry.Namespace,
			entry.ID, e.Role, ext.EntryDate); err != nil {
			return err
		}
	}
	return nil
}

func (l *layerImpl) deriveThread(ctx context.Context, entry Entry) error {
	ext := ExtractThread(entry)
	if ext.ExternalID == "" {
		return nil
	}
	stored, err := l.store.UpsertThread(ctx, Thread{
		Namespace:  entry.Namespace,
		ExternalID: ext.ExternalID,
		Subject:    ext.Subject,
		FirstSeen:  ext.EntryDate,
		LastSeen:   ext.EntryDate,
	})
	if err != nil {
		return err
	}
	return l.store.LinkThreadEntry(ctx, stored.ID, entry.Namespace,
		entry.ID, ext.EntryDate)
}

// composeAdapterID prefixes the entry id with its namespace so
// distinct namespaces can use the same per-source id ("msg-1" in
// gmail-personal and gmail-work are different rows in the adapter).
func composeAdapterID(namespace, id string) string {
	return namespace + ":" + id
}

// withNamespace ensures the namespace + entry id are present in the
// metadata map written to the adapter, even when callers forgot to
// set them. Layer-derived views read namespace from these fields.
func withNamespace(metadata map[string]any, namespace, id string) map[string]any {
	out := make(map[string]any, len(metadata)+2)
	for k, v := range metadata {
		out[k] = v
	}
	if _, ok := out["namespace"]; !ok {
		out["namespace"] = namespace
	}
	if _, ok := out["entry_id"]; !ok {
		out["entry_id"] = id
	}
	return out
}

// Compile-time assertion that memory.Adapter satisfies our narrow
// Adapter interface. Catches drift if the adapter SPI changes.
var _ Adapter = (memory.Adapter)(nil)

// Compile-time assertion that the package's narrow Adapter and the
// upstream memory.Adapter agree on method signatures. We construct
// a zero-value interface to type-check; never call it.
var _ = func() Adapter {
	var x memory.Adapter
	return x
}

// ExplainNoEntities describes, for debugging, why an entry's
// resolver returned zero entities. Production code doesn't call
// this; left exported so debugging scripts can use it.
func ExplainNoEntities(entry Entry) string {
	if entry.Metadata == nil {
		return "no metadata on entry"
	}
	from := stringFromMetadata(entry.Metadata, "from")
	to := stringFromMetadata(entry.Metadata, "to")
	if from == "" && to == "" {
		return "no from/to headers in metadata"
	}
	addrs := append(parseAddresses(from), parseAddresses(to)...)
	if len(addrs) == 0 {
		return "from/to present but parsed to zero addresses (malformed header?)"
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.Address)
	}
	return "addresses found: " + strings.Join(parts, ", ")
}
