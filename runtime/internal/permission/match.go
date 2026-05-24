package permission

import (
	"path"
	"strings"
	"time"
)

// matchScope returns true if attempt satisfies every constraint that
// scope places under the given schema. Fields not present in scope
// impose no constraint; fields present in scope but absent from
// attempt are denied (the attempt must positively specify what
// dimension it's accessing).
//
// reason describes why a mismatch happened; empty on match.
func matchScope(schema ScopeSchema, scope, attempt map[string]any) (ok bool, reason string) {
	for field, primitive := range schema {
		scopeVal, scopeHas := scope[field]
		attemptVal, attemptHas := attempt[field]

		if !scopeHas {
			// Scope leaves this dimension unconstrained.
			continue
		}
		if !attemptHas {
			return false, "attempt missing dimension " + field + " constrained by scope"
		}
		if !matchValue(primitive, scopeVal, attemptVal) {
			return false, "scope " + field + " rejects attempted value"
		}
	}
	return true, ""
}

// matchValue dispatches on the primitive. Unknown primitives reject
// (defensive — registered capabilities should only use known primitives,
// but a future spec might introduce one before the engine knows it).
func matchValue(p MatchPrimitive, scopeVal, attemptVal any) bool {
	switch p {
	case MatchEquals:
		return matchEquals(scopeVal, attemptVal)
	case MatchGlobList:
		return matchGlobList(scopeVal, attemptVal)
	case MatchPrefix:
		return matchPrefix(scopeVal, attemptVal)
	case MatchSetIntersect:
		return matchSetIntersect(scopeVal, attemptVal)
	case MatchSetSubset:
		return matchSetSubset(scopeVal, attemptVal)
	case MatchSetExcludes:
		return matchSetExcludes(scopeVal, attemptVal)
	case MatchRangeIncludes:
		return matchRangeIncludes(scopeVal, attemptVal)
	case MatchSenderGlob:
		return matchSenderGlob(scopeVal, attemptVal)
	}
	return false
}

// --- Individual primitives --------------------------------------------

func matchEquals(scope, attempt any) bool {
	s, ok := asString(scope)
	if !ok {
		return false
	}
	a, ok := asString(attempt)
	if !ok {
		return false
	}
	return s == a
}

func matchGlobList(scope, attempt any) bool {
	globs, ok := asStringList(scope)
	if !ok {
		// Allow a bare string scope too — convenient for single-glob configs.
		if g, isStr := asString(scope); isStr {
			globs = []string{g}
		} else {
			return false
		}
	}
	a, ok := asString(attempt)
	if !ok {
		return false
	}
	for _, g := range globs {
		if globMatch(g, a) {
			return true
		}
	}
	return false
}

func matchPrefix(scope, attempt any) bool {
	prefix, ok := asString(scope)
	if !ok {
		return false
	}
	a, ok := asString(attempt)
	if !ok {
		return false
	}
	return strings.HasPrefix(a, prefix)
}

func matchSetIntersect(scope, attempt any) bool {
	s, ok := asStringList(scope)
	if !ok {
		return false
	}
	a, ok := asStringList(attempt)
	if !ok {
		// Also accept a single string as a one-element set.
		if singleton, isStr := asString(attempt); isStr {
			a = []string{singleton}
		} else {
			return false
		}
	}
	return hasIntersection(s, a)
}

func matchSetSubset(scope, attempt any) bool {
	scopeSet, ok := asStringList(scope)
	if !ok {
		return false
	}
	attemptList, ok := asStringList(attempt)
	if !ok {
		if singleton, isStr := asString(attempt); isStr {
			attemptList = []string{singleton}
		} else {
			return false
		}
	}
	allowed := make(map[string]struct{}, len(scopeSet))
	for _, s := range scopeSet {
		allowed[s] = struct{}{}
	}
	for _, x := range attemptList {
		if _, ok := allowed[x]; !ok {
			return false
		}
	}
	return true
}

func matchSetExcludes(scope, attempt any) bool {
	excluded, ok := asStringList(scope)
	if !ok {
		return false
	}
	attemptList, ok := asStringList(attempt)
	if !ok {
		if singleton, isStr := asString(attempt); isStr {
			attemptList = []string{singleton}
		} else {
			return false
		}
	}
	return !hasIntersection(excluded, attemptList)
}

func matchRangeIncludes(scope, attempt any) bool {
	since, until, hasSince, hasUntil := asTimeRange(scope)
	t, ok := asTime(attempt)
	if !ok {
		return false
	}
	if hasSince && t.Before(since) {
		return false
	}
	// Until is treated as exclusive — the spec says "on or before"
	// but using After for the negation matches that: t.After(until)
	// only when t > until.
	if hasUntil && t.After(until) {
		return false
	}
	return true
}

// matchSenderGlob is currently identical to a single-pattern glob
// match. It exists as a distinct primitive so future versions can
// add email-aware semantics (local-part vs domain matching, MX
// validation, etc.) without changing the calling capabilities.
func matchSenderGlob(scope, attempt any) bool {
	pattern, ok := asString(scope)
	if !ok {
		return false
	}
	a, ok := asString(attempt)
	if !ok {
		return false
	}
	return globMatch(pattern, a)
}

// --- Helpers ---------------------------------------------------------

// globMatch is the shared glob implementation: path.Match handles
// `*` (any sequence excluding `/`) and `?` (single char). For glob
// patterns intended to span path separators, callers should use
// multiple patterns ([]string in MatchGlobList) rather than `**`.
func globMatch(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	if err != nil {
		return false
	}
	return matched
}

func hasIntersection(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}

// asString returns v as a string, or false if the type doesn't match.
func asString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// asStringList accepts either []any (JSON-decoded array of strings)
// or []string (Go-native).
func asStringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// asTime parses an RFC3339(Nano) timestamp string into a time.Time.
// Accepts time.Time directly too (for tests constructing values).
func asTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// asTimeRange extracts {since, until} from a scope value. Either or
// both bounds may be absent.
func asTimeRange(v any) (since, until time.Time, hasSince, hasUntil bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if s, ok := m["since"]; ok {
		if t, ok := asTime(s); ok {
			since = t
			hasSince = true
		}
	}
	if u, ok := m["until"]; ok {
		if t, ok := asTime(u); ok {
			until = t
			hasUntil = true
		}
	}
	return
}
