package permission

import (
	"sort"
	"testing"
)

// TestReservedNamespaces_DocumentedHere pins the contract that the
// capsule package's reservedCapsulePrefixes / reservedCapsuleExceptions
// lists must match these. The two lists are intentionally duplicated
// (capsule shouldn't import permission for offline `capsule validate`
// to work without a configured runtime), but they MUST stay in sync.
//
// This test fails when the lists drift. It does NOT import the
// capsule package — that would defeat the purpose of duplication.
// Instead, it asserts the prefixes and exceptions that live in this
// file. The capsule package's manifest_sync_test.go asserts the
// same set from its side.
func TestReservedNamespaces_DocumentedHere(t *testing.T) {
	wantPrefixes := []string{
		"audit.",
		"loamss.",
		"pairing.",
		"permission.",
		"runtime.",
	}
	gotPrefixes := append([]string(nil), reservedNamespaces...)
	sort.Strings(gotPrefixes)
	if len(gotPrefixes) != len(wantPrefixes) {
		t.Fatalf("prefix count: got %v, want %v", gotPrefixes, wantPrefixes)
	}
	for i := range wantPrefixes {
		if gotPrefixes[i] != wantPrefixes[i] {
			t.Errorf("prefix[%d]: got %q, want %q", i, gotPrefixes[i], wantPrefixes[i])
		}
	}

	wantExceptions := map[string]bool{"audit.read": true}
	if len(reservedExceptions) != len(wantExceptions) {
		t.Errorf("exception count: got %v, want %v", reservedExceptions, wantExceptions)
	}
	for k, v := range wantExceptions {
		if reservedExceptions[k] != v {
			t.Errorf("exception %q: got %v, want %v", k, reservedExceptions[k], v)
		}
	}
}

func TestIsReservedNamespace_Exported(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"audit.write", true},
		{"audit.read", false}, // exception
		{"permission.grant", true},
		{"pairing.complete", true},
		{"runtime.shutdown", true},
		{"loamss.config", true},
		{"memory.read", false},
		{"email.send", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsReservedNamespace(tc.name); got != tc.want {
			t.Errorf("IsReservedNamespace(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
