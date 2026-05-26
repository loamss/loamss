package cli

import (
	"os"
	"testing"

	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/oauth"
)

// TestMain wires package-level hooks the test binary needs.
//
// capsule.WellKnownOAuthProvider is consulted by the manifest
// validator. In production it's set in start.go to oauth.WellKnown;
// in tests we wire the same plumbing so the calendar-ingestor
// fixture (provider: google) validates cleanly without manifest
// inline endpoints.
func TestMain(m *testing.M) {
	capsule.WellKnownOAuthProvider = oauth.WellKnown
	os.Exit(m.Run())
}
