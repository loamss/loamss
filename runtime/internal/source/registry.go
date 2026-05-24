package source

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a new (uninitialized) Source. Each source
// package registers a Factory in its init() function:
//
//	func init() {
//	    source.Register("source:gmail", func() source.Source {
//	        return &gmailSource{}
//	    })
//	}
//
// Init is called by the runtime after construction with the
// user-supplied Deps.
type Factory func() Source

var registry = struct {
	mu        sync.RWMutex
	factories map[string]Factory
}{
	factories: make(map[string]Factory),
}

// Register makes a source implementation available by id. Source
// packages call this from init(). Re-registering an id panics —
// source ids are global identifiers and silent overwriting would
// hide real bugs.
//
// The expected id format is "source:<name>" (e.g., "source:gmail").
// Register does not enforce this; the config validator and the
// CLI's `source add` do. Keeping Register lenient simplifies
// test setup.
func Register(id string, f Factory) {
	if f == nil {
		panic("source: Register called with nil Factory for " + id)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.factories[id]; exists {
		panic("source: adapter " + id + " registered twice")
	}
	registry.factories[id] = f
}

// New looks up the factory for id and returns a freshly-constructed,
// uninitialized Source. The caller is expected to Init the source
// with the user-supplied Deps before using it.
func New(id string) (Source, error) {
	registry.mu.RLock()
	f, ok := registry.factories[id]
	registry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSource, id)
	}
	return f(), nil
}

// Registered returns the ids of all currently-registered sources,
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
