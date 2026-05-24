package permission

import (
	"testing"
	"time"
)

func TestMatchEquals(t *testing.T) {
	cases := []struct {
		name           string
		scope, attempt any
		want           bool
	}{
		{"equal", "foo", "foo", true},
		{"different", "foo", "bar", false},
		{"scope-not-string", 42, "foo", false},
		{"attempt-not-string", "foo", 42, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchEquals(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("matchEquals(%v, %v) = %v, want %v", tc.scope, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestMatchGlobList(t *testing.T) {
	cases := []struct {
		name           string
		scope, attempt any
		want           bool
	}{
		{"prefix-glob match", []any{"finance/*"}, "finance/receipt.json", true},
		{"prefix-glob no match", []any{"finance/*"}, "personal/note.txt", false},
		{"multiple globs first matches", []any{"finance/*", "tax/*"}, "finance/r.json", true},
		{"multiple globs second matches", []any{"finance/*", "tax/*"}, "tax/r.json", true},
		{"multiple globs none match", []any{"finance/*", "tax/*"}, "other/x.json", false},
		{"single bare-string scope", "finance/*", "finance/r.json", true},
		{"glob doesn't cross slash by default", []any{"finance/*"}, "finance/sub/r.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchGlobList(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("matchGlobList(%v, %v) = %v, want %v", tc.scope, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestMatchPrefix(t *testing.T) {
	if !matchPrefix("loamss://content/", "loamss://content/video/abc") {
		t.Error("prefix should match")
	}
	if matchPrefix("loamss://content/", "loamss://memory/x") {
		t.Error("prefix should not match")
	}
}

func TestMatchSetIntersect(t *testing.T) {
	cases := []struct {
		name           string
		scope, attempt any
		want           bool
	}{
		{"overlap", []any{"a", "b", "c"}, []any{"b", "d"}, true},
		{"no overlap", []any{"a", "b"}, []any{"c", "d"}, false},
		{"empty scope", []any{}, []any{"a"}, false},
		{"empty attempt", []any{"a"}, []any{}, false},
		{"singleton attempt as string", []any{"people", "projects"}, "people", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSetIntersect(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("matchSetIntersect: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchSetSubset(t *testing.T) {
	cases := []struct {
		name           string
		scope, attempt any
		want           bool
	}{
		{"subset", []any{"a", "b", "c"}, []any{"a", "b"}, true},
		{"equal", []any{"a", "b"}, []any{"a", "b"}, true},
		{"empty attempt", []any{"a", "b"}, []any{}, true},
		{"superset", []any{"a"}, []any{"a", "b"}, false},
		{"disjoint", []any{"a"}, []any{"b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSetSubset(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("matchSetSubset: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchSetExcludes(t *testing.T) {
	cases := []struct {
		name           string
		scope, attempt any
		want           bool
	}{
		{"disjoint - allowed", []any{"health", "financial"}, []any{"public", "general"}, true},
		{"overlap - rejected", []any{"health"}, []any{"public", "health"}, false},
		{"empty scope means nothing excluded", []any{}, []any{"anything"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSetExcludes(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("matchSetExcludes: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchRangeIncludes(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		scope   map[string]any
		attempt time.Time
		want    bool
	}{
		{"in range", map[string]any{"since": since.Format(time.RFC3339Nano), "until": until.Format(time.RFC3339Nano)}, mid, true},
		{"before since", map[string]any{"since": since.Format(time.RFC3339Nano)}, before, false},
		{"after until", map[string]any{"until": until.Format(time.RFC3339Nano)}, after, false},
		{"only since (open-ended)", map[string]any{"since": since.Format(time.RFC3339Nano)}, after, true},
		{"only until (open-ended)", map[string]any{"until": until.Format(time.RFC3339Nano)}, before, true},
		{"no bounds (always)", map[string]any{}, mid, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchRangeIncludes(tc.scope, tc.attempt.Format(time.RFC3339Nano))
			if got != tc.want {
				t.Errorf("matchRangeIncludes: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchSenderGlob(t *testing.T) {
	cases := []struct {
		scope, attempt string
		want           bool
	}{
		{"sarah@acme.com", "sarah@acme.com", true},
		{"*@acme.com", "sarah@acme.com", true},
		{"*@acme.com", "alex@other.com", false},
		{"sarah@*", "sarah@acme.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.scope+"-vs-"+tc.attempt, func(t *testing.T) {
			if got := matchSenderGlob(tc.scope, tc.attempt); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchScope_EmptyScopeMatchesAnything(t *testing.T) {
	schema := ScopeSchema{
		"sender": MatchSenderGlob,
	}
	// Empty scope = no constraint on sender = allow.
	ok, _ := matchScope(schema, map[string]any{}, map[string]any{"sender": "anyone@example.com"})
	if !ok {
		t.Error("empty scope should match anything")
	}
}

func TestMatchScope_AttemptMissingFieldIsDenied(t *testing.T) {
	schema := ScopeSchema{
		"sender": MatchSenderGlob,
	}
	ok, reason := matchScope(schema,
		map[string]any{"sender": "*@acme.com"},
		map[string]any{}) // attempt doesn't specify sender
	if ok {
		t.Error("attempt missing constrained field should be denied")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestMatchScope_FullMemoryQueryExample(t *testing.T) {
	// Mirror permission-model.md scenario 1: ChatGPT memory.query
	// scoped to entities=[people, projects], data_classes_excluded=[health].
	//
	// The attempt mirrors a query that touches "people" entities and
	// asserts the access does NOT include the health data class. With
	// MatchSetIntersect on entities, "people" intersects [people, projects].
	// With MatchSetExcludes on data_classes_excluded, an empty attempt set
	// is disjoint from [health] → allowed.
	schema := ScopeSchema{
		"entities":              MatchSetIntersect,
		"data_classes_excluded": MatchSetExcludes,
	}
	scope := map[string]any{
		"entities":              []any{"people", "projects"},
		"data_classes_excluded": []any{"health"},
	}
	ok, reason := matchScope(schema, scope, map[string]any{
		"entities":              []any{"people"},
		"data_classes_excluded": []any{},
	})
	if !ok {
		t.Errorf("should allow people query without health overlap; reason: %s", reason)
	}

	// Denied: query attempt actually touches the "health" class. Since
	// the scope forbids it (MatchSetExcludes rejects overlap), the
	// matcher returns false.
	ok, _ = matchScope(schema, scope, map[string]any{
		"entities":              []any{"people"},
		"data_classes_excluded": []any{"health"},
	})
	if ok {
		t.Error("should deny when attempt's classes overlap the scope's excluded set")
	}
}

func TestMatchValue_UnknownPrimitiveRejects(t *testing.T) {
	if matchValue(MatchPrimitive("bogus"), "foo", "foo") {
		t.Error("unknown primitive should not match")
	}
}
