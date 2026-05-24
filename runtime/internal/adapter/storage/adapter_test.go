package storage

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrors_AreDistinct(t *testing.T) {
	// Each sentinel must be uniquely identifiable via errors.Is.
	if errors.Is(ErrNotFound, ErrUnsupported) {
		t.Error("ErrNotFound and ErrUnsupported should be distinct")
	}
	if errors.Is(ErrUnsupported, ErrNotFound) {
		t.Error("ErrUnsupported and ErrNotFound should be distinct")
	}
	if errors.Is(ErrConnectionLost, ErrNotFound) {
		t.Error("ErrConnectionLost should be distinct from ErrNotFound")
	}
}

func TestErrors_Wrappable(t *testing.T) {
	// Adapters wrap sentinel errors with operational context;
	// callers test with errors.Is. Make sure that pattern works.
	wrapped := fmt.Errorf("reading config.yaml: %w", ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Errorf("wrapped error should still match via errors.Is")
	}
}

func TestOpConstants_AreStrings(t *testing.T) {
	// Op values are deliberately strings — debuggability when an
	// adapter logs the op it was asked to perform.
	if string(OpRead) != "read" {
		t.Errorf("OpRead string form: got %q, want %q", string(OpRead), "read")
	}
	if string(OpWrite) != "write" {
		t.Errorf("OpWrite string form: got %q, want %q", string(OpWrite), "write")
	}
}
