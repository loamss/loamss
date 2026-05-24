package storage

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

// noopAdapter satisfies the Adapter interface with do-nothing methods.
// Used as a stand-in by registry tests.
type noopAdapter struct{}

func (noopAdapter) Init(context.Context, map[string]any) error { return nil }
func (noopAdapter) Read(context.Context, string) ([]byte, error) {
	return nil, ErrNotFound
}
func (noopAdapter) ReadStream(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, ErrNotFound
}
func (noopAdapter) Write(context.Context, string, []byte) error          { return nil }
func (noopAdapter) WriteStream(context.Context, string, io.Reader) error { return nil }
func (noopAdapter) Delete(context.Context, string) error                 { return nil }
func (noopAdapter) Exists(context.Context, string) (bool, error)         { return false, nil }
func (noopAdapter) Metadata(context.Context, string) (ObjectMetadata, error) {
	return ObjectMetadata{}, ErrNotFound
}
func (noopAdapter) List(context.Context, string) (<-chan ListEntry, error) {
	ch := make(chan ListEntry)
	close(ch)
	return ch, nil
}
func (noopAdapter) SignedURL(context.Context, string, time.Duration, Op) (string, error) {
	return "", ErrUnsupported
}
func (noopAdapter) HealthCheck(context.Context) error { return nil }
func (noopAdapter) Close(context.Context) error       { return nil }

func TestRegister_NewRoundTrip(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("storage:test", func() Adapter { return noopAdapter{} })

	a, err := New("storage:test")
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

	_, err := New("storage:does-not-exist")
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

	Register("storage:dup", func() Adapter { return noopAdapter{} })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register("storage:dup", func() Adapter { return noopAdapter{} })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil factory")
		}
	}()
	Register("storage:nil-factory", nil)
}

func TestRegistered_ReturnsSortedIDs(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	for _, id := range []string{"storage:c", "storage:a", "storage:b"} {
		Register(id, func() Adapter { return noopAdapter{} })
	}

	got := Registered()
	want := []string{"storage:a", "storage:b", "storage:c"}
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

	Register("storage:concurrent", func() Adapter { return noopAdapter{} })

	// Hammer New and Registered from multiple goroutines. Race detector
	// will catch any unsynchronized access.
	const goroutines = 20
	const iterations = 100
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < iterations; j++ {
				_, _ = New("storage:concurrent")
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
