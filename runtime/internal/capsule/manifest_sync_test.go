package capsule

import (
	"sort"
	"testing"
)

// TestReservedNamespaces_LocalMirror is the capsule-package half of
// the cross-package sync pinning. The permission package's
// canonical_sync_test.go asserts the same set from its side. If
// these two tests diverge, the lists have drifted — re-sync before
// merging.
//
// The duplication is intentional: the capsule package must validate
// manifests offline (no runtime configured) and importing permission
// would force every capsule-validate invocation to load the
// permission package + its SQLite driver. Two tiny lists is the
// price of keeping `loamss capsule validate` standalone.
func TestReservedNamespaces_LocalMirror(t *testing.T) {
	wantPrefixes := []string{
		"audit.",
		"loamss.",
		"pairing.",
		"permission.",
		"runtime.",
	}
	gotPrefixes := append([]string(nil), reservedCapsulePrefixes...)
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
	if len(reservedCapsuleExceptions) != len(wantExceptions) {
		t.Errorf("exception count: got %v, want %v", reservedCapsuleExceptions, wantExceptions)
	}
	for k, v := range wantExceptions {
		if reservedCapsuleExceptions[k] != v {
			t.Errorf("exception %q: got %v, want %v", k, reservedCapsuleExceptions[k], v)
		}
	}
}
