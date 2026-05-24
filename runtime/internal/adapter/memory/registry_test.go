package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// noopAdapter satisfies the Adapter interface with do-nothing methods.
// Used as a stand-in by registry tests.
type noopAdapter struct{}

func (noopAdapter) Init(context.Context, map[string]any) error { return nil }
func (noopAdapter) Upsert(context.Context, string, []float32, map[string]any) error {
	return nil
}
func (noopAdapter) BatchUpsert(context.Context, []Entry) error { return nil }
func (noopAdapter) Get(context.Context, string) (*Entry, error) {
	return nil, ErrNotFound
}
func (noopAdapter) Search(context.Context, []float32, int, MetadataFilter) ([]SearchHit, error) {
	return nil, nil
}
func (noopAdapter) Delete(context.Context, string) error { return nil }
func (noopAdapter) Stats(context.Context) (Stats, error) { return Stats{}, nil }
func (noopAdapter) HealthCheck(context.Context) error    { return nil }
func (noopAdapter) Close(context.Context) error          { return nil }

func TestRegister_NewRoundTrip(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("memory:test", func() Adapter { return noopAdapter{} })

	a, err := New("memory:test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil adapter")
	}
	if _, ok := a.(noopAdapter); !ok {
		t.Errorf("New returned wrong adapter type: %T", a)
	}
}

func TestNew_UnknownAdapter(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	_, err := New("memory:does-not-exist")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUnknownAdapter) {
		t.Errorf("error should wrap ErrUnknownAdapter, got: %v", err)
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("memory:dup", func() Adapter { return noopAdapter{} })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register("memory:dup", func() Adapter { return noopAdapter{} })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil factory")
		}
	}()
	Register("memory:nil-factory", nil)
}

func TestRegistered_ReturnsSortedIDs(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	for _, id := range []string{"memory:c", "memory:a", "memory:b"} {
		Register(id, func() Adapter { return noopAdapter{} })
	}

	got := Registered()
	want := []string{"memory:a", "memory:b", "memory:c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Registered(): got %v, want %v", got, want)
	}
}

func TestRegistered_EmptyWhenNoRegistrations(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	got := Registered()
	if len(got) != 0 {
		t.Errorf("Registered() should be empty after reset, got: %v", got)
	}
}

func TestRegistry_ConcurrentSafe(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("memory:concurrent", func() Adapter { return noopAdapter{} })

	const goroutines = 20
	const iterations = 100
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < iterations; j++ {
				_, _ = New("memory:concurrent")
				_ = Registered()
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

// Compile-time check that noopAdapter satisfies Adapter. If the
// interface changes and noopAdapter falls out of sync, this build
// breaks loudly.
var _ Adapter = noopAdapter{}
