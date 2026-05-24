package storage

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a new (uninitialized) Adapter. Each adapter
// package registers a Factory in its init() function:
//
//	func init() {
//	    storage.Register("storage:fs-encrypted", func() storage.Adapter {
//	        return &fsEncrypted{}
//	    })
//	}
//
// Init is called by the runtime after construction with the
// user-supplied config map.
type Factory func() Adapter

// ErrUnknownAdapter is returned by New when the requested adapter
// id has not been registered.
var ErrUnknownAdapter = errors.New("storage: unknown adapter")

var registry = struct {
	mu        sync.RWMutex
	factories map[string]Factory
}{
	factories: make(map[string]Factory),
}

// Register makes a storage adapter implementation available by id.
// Adapter packages call this from init(). Re-registering an id panics
// — adapter ids are global identifiers and silent overwriting would
// hide real bugs.
//
// The expected id format is "storage:<name>" (e.g.,
// "storage:fs-encrypted"). Register does not enforce this; the
// config validator does. Keeping Register lenient makes test setup
// simpler.
func Register(id string, f Factory) {
	if f == nil {
		panic("storage: Register called with nil Factory for " + id)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.factories[id]; exists {
		panic("storage: adapter " + id + " registered twice")
	}
	registry.factories[id] = f
}

// New looks up the factory for id and returns a freshly-constructed,
// uninitialized Adapter. The caller is expected to Init the adapter
// with the user-supplied config before using it.
func New(id string) (Adapter, error) {
	registry.mu.RLock()
	f, ok := registry.factories[id]
	registry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAdapter, id)
	}
	return f(), nil
}

// Registered returns the ids of all currently-registered adapters,
// sorted lexicographically. Used by `loamss doctor` and by tests.
func Registered() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	ids := make([]string, 0, len(registry.factories))
	for id := range registry.factories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// resetRegistry clears the registry. Package-private; tests use it
// to start each case from a known state. Never called from
// production code.
func resetRegistry() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.factories = make(map[string]Factory)
}
