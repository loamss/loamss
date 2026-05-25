package pgvector

import (
	"testing"
)

// Unit coverage for the Cloud SQL config path. The real Cloud SQL
// Connector requires a live GCP project + IAM creds; testing the
// actual dial path is integration territory and gated by
// LOAMSS_CLOUDSQL_TEST_INSTANCE if/when we want to wire it.

func TestOptionalBool_AcceptsVariousShapes(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want bool
	}{
		{"absent", nil, false}, // sentinel for "not in map"
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"string 1", "1", true},
		{"string 0", "0", false},
		{"string yes", "yes", true},
		{"int 1", 1, true},
		{"int 0", 0, false},
		{"float64 0", float64(0), false},
		{"float64 1", float64(1), true},
		{"weird type", []string{"x"}, false}, // ignored, fallback
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := map[string]any{}
			if c.name != "absent" {
				config["k"] = c.val
			}
			got := optionalBool(config, "k", false)
			if got != c.want {
				t.Errorf("optionalBool(%v) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}
