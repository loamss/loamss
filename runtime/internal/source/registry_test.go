package source

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// minimal in-package test source so we don't import the sourcetest
// sub-package from this file (that would create an import cycle
// since sourcetest imports source).
type stubSource struct{ id string }

func (s *stubSource) ID() string                                     { return s.id }
func (s *stubSource) Init(context.Context, Deps) error               { return nil }
func (s *stubSource) AuthStatus(context.Context) (AuthStatus, error) { return AuthStatus{}, nil }
func (s *stubSource) BeginAuth(context.Context) (AuthFlow, error)    { return AuthFlow{}, nil }
func (s *stubSource) CompleteAuth(context.Context, map[string]string) error {
	return nil
}
func (s *stubSource) Sync(context.Context, []byte) (SyncResult, error) { return SyncResult{}, nil }
func (s *stubSource) HealthCheck(context.Context) error                { return nil }
func (s *stubSource) Close(context.Context) error                      { return nil }

func TestRegister_NewLookup(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	want := &stubSource{id: "source:test"}
	Register("source:test", func() Source { return want })

	got, err := New("source:test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got != want {
		t.Errorf("got %p, want %p", got, want)
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("source:dup", func() Source { return &stubSource{id: "source:dup"} })
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate Register")
		}
	}()
	Register("source:dup", func() Source { return &stubSource{id: "source:dup"} })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil factory")
		}
	}()
	Register("source:nil", nil)
}

func TestNew_UnknownReturnsSentinel(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	_, err := New("source:nope")
	if !errors.Is(err, ErrUnknownSource) {
		t.Errorf("err=%v, want ErrUnknownSource", err)
	}
}

func TestRegistered_SortsLexicographically(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	Register("source:zebra", func() Source { return &stubSource{id: "source:zebra"} })
	Register("source:apple", func() Source { return &stubSource{id: "source:apple"} })
	Register("source:mango", func() Source { return &stubSource{id: "source:mango"} })

	got := Registered()
	want := []string{"source:apple", "source:mango", "source:zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
