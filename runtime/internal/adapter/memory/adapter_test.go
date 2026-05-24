package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestErrors_AreDistinct(t *testing.T) {
	sentinels := []error{
		ErrNotFound,
		ErrDimensionMismatch,
		ErrConnectionLost,
		ErrUnsupported,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel %v should not match %v", a, b)
			}
		}
	}
}

func TestErrors_Wrappable(t *testing.T) {
	// Adapters wrap sentinel errors with operational context;
	// callers test with errors.Is.
	wrapped := fmt.Errorf("getting entity %q: %w", "abc123", ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Errorf("wrapped error should still match via errors.Is")
	}
}

func TestEntry_JSONRoundTrip(t *testing.T) {
	e := Entry{
		ID:       "ent-01HVZ",
		Vector:   []float32{1, 2, 3, 4},
		Metadata: map[string]any{"type": "person", "name": "Sarah"},
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, data)
	}
	if got.ID != e.ID {
		t.Errorf("id: %q vs %q", got.ID, e.ID)
	}
	if len(got.Vector) != len(e.Vector) {
		t.Errorf("vector length: %d vs %d", len(got.Vector), len(e.Vector))
	}
}

func TestSearchHit_JSONShape(t *testing.T) {
	h := SearchHit{
		ID:       "ent-01HVZ",
		Distance: 0.42,
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Distance should always be present (no omitempty); Vector and
	// Metadata are conditionally absent.
	if want := `"distance":0.42`; !contains(string(data), want) {
		t.Errorf("expected %s in %s", want, data)
	}
}

func TestMetadataFilter_NilEqualsMatchesAll(t *testing.T) {
	// Just an invariant we want to preserve at the type level — a
	// MetadataFilter with no fields set is equivalent to "no filter".
	// Adapter implementations rely on this for the unfiltered path.
	f := MetadataFilter{}
	if f.Equals != nil {
		t.Errorf("zero MetadataFilter should have nil Equals; got %v", f.Equals)
	}
}

func TestStats_DimensionDefault(t *testing.T) {
	// Stats with no vectors written should reasonably have Dimension=0.
	// Adapters lazy-set this; the runtime should not rely on a
	// specific default before any writes.
	s := Stats{}
	if s.Dimension != 0 {
		t.Errorf("default Dimension should be 0, got %d", s.Dimension)
	}
	if s.Count != 0 {
		t.Errorf("default Count should be 0, got %d", s.Count)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) <= len(haystack) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
