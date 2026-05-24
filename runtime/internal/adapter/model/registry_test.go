package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeAdapter satisfies the Adapter interface with the minimum
// behavior we need for registry-level tests. Lives here (not in a
// sub-package) because Register/New are package-private writers
// and we want this file to share that scope.
type fakeAdapter struct{ id string }

func (f fakeAdapter) Init(context.Context, map[string]any) error { return nil }
func (f fakeAdapter) Models(context.Context) ([]Descriptor, error) {
	return []Descriptor{{ID: f.id}}, nil
}
func (f fakeAdapter) Generate(context.Context, GenerateRequest) (*GenerateResponse, error) {
	return nil, ErrGenerateNotSupported
}
func (f fakeAdapter) GenerateStream(context.Context, GenerateRequest) (<-chan GenerateChunk, error) {
	return nil, ErrGenerateNotSupported
}
func (f fakeAdapter) Embed(context.Context, EmbedRequest) (*EmbedResponse, error) {
	return nil, ErrEmbedNotSupported
}
func (f fakeAdapter) EstimateCost(context.Context, GenerateRequest) (Cost, error) {
	return Cost{}, nil
}
func (f fakeAdapter) HealthCheck(context.Context) error { return nil }
func (f fakeAdapter) Close(context.Context) error       { return nil }

func TestRegister_NewRoundTrip(t *testing.T) {
	resetRegistry()
	Register("model:fake-A", func() Adapter { return fakeAdapter{id: "A"} })
	a, err := New("model:fake-A")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fa, ok := a.(fakeAdapter)
	if !ok || fa.id != "A" {
		t.Errorf("New returned wrong adapter: %T %+v", a, a)
	}
}

func TestNew_UnknownAdapter(t *testing.T) {
	resetRegistry()
	_, err := New("model:nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnknownAdapter) {
		t.Errorf("expected ErrUnknownAdapter, got: %v", err)
	}
	if !strings.Contains(err.Error(), "model:nope") {
		t.Errorf("error should mention the requested id, got: %v", err)
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	resetRegistry()
	Register("model:dup", func() Adapter { return fakeAdapter{} })
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	Register("model:dup", func() Adapter { return fakeAdapter{} })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	resetRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil factory")
		}
	}()
	Register("model:nil", nil)
}

func TestRegistered_ReturnsSortedIDs(t *testing.T) {
	resetRegistry()
	Register("model:b", func() Adapter { return fakeAdapter{} })
	Register("model:a", func() Adapter { return fakeAdapter{} })
	Register("model:c", func() Adapter { return fakeAdapter{} })
	ids := Registered()
	want := []string{"model:a", "model:b", "model:c"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("Registered()[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestRegistered_EmptyWhenNoRegistrations(t *testing.T) {
	resetRegistry()
	if ids := Registered(); len(ids) != 0 {
		t.Errorf("expected empty registry, got %v", ids)
	}
}

func TestRegistry_ConcurrentSafe(t *testing.T) {
	t.Helper()
	resetRegistry()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			Register("model:concurrent-"+string(rune('a'+id%26))+string(rune('a'+id/26)),
				func() Adapter { return fakeAdapter{} })
		}(i)
		go func() {
			defer wg.Done()
			_ = Registered()
		}()
	}
	wg.Wait()
}
