package profile

import (
	"strings"
	"testing"
)

// emptyEnv simulates "no relevant env vars set."
func emptyEnv(string) string { return "" }

// envWith returns a lookup that returns the given value for the
// given key, "" otherwise.
func envWith(name, value string) func(string) string {
	return func(k string) string {
		if k == name {
			return value
		}
		return ""
	}
}

func TestResolve_ConfigWinsOverEverything(t *testing.T) {
	det, err := Resolve("cloud", "local", envWith("K_SERVICE", "x"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if det.Profile != Cloud {
		t.Errorf("Profile = %q, want %q", det.Profile, Cloud)
	}
	if det.Source != SourceConfig {
		t.Errorf("Source = %q, want %q", det.Source, SourceConfig)
	}
}

func TestResolve_EnvVarWinsOverAutodetect(t *testing.T) {
	det, err := Resolve("", "local", envWith("K_SERVICE", "x"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if det.Profile != Local {
		t.Errorf("Profile = %q, want %q", det.Profile, Local)
	}
	if det.Source != SourceEnvVar {
		t.Errorf("Source = %q, want %q", det.Source, SourceEnvVar)
	}
}

func TestResolve_AutodetectsCloudPlatforms(t *testing.T) {
	cases := []struct {
		envVar   string
		platform string
	}{
		{"K_SERVICE", "Cloud Run"},
		{"KUBERNETES_SERVICE_HOST", "Kubernetes"},
		{"FLY_APP_NAME", "Fly.io"},
		{"RENDER", "Render"},
		{"RAILWAY_ENVIRONMENT", "Railway"},
	}
	for _, tc := range cases {
		t.Run(tc.envVar, func(t *testing.T) {
			det, err := Resolve("", "", envWith(tc.envVar, "anything"))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if det.Profile != Cloud {
				t.Errorf("Profile = %q, want %q", det.Profile, Cloud)
			}
			if det.Source != SourceDetect {
				t.Errorf("Source = %q, want %q", det.Source, SourceDetect)
			}
			if !strings.Contains(det.Detail, tc.envVar) {
				t.Errorf("Detail = %q should mention %q", det.Detail, tc.envVar)
			}
		})
	}
}

func TestResolve_DefaultsToLocal(t *testing.T) {
	det, err := Resolve("", "", emptyEnv)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if det.Profile != Local {
		t.Errorf("Profile = %q, want %q", det.Profile, Local)
	}
	if det.Source != SourceDefault {
		t.Errorf("Source = %q, want %q", det.Source, SourceDefault)
	}
}

func TestResolve_RejectsUnknownConfigValue(t *testing.T) {
	_, err := Resolve("mainframe", "", emptyEnv)
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if !strings.Contains(err.Error(), "mainframe") {
		t.Errorf("error should mention the bad value: %v", err)
	}
}

func TestResolve_RejectsUnknownEnvVarValue(t *testing.T) {
	_, err := Resolve("", "thunderdome", emptyEnv)
	if err == nil {
		t.Fatal("expected error for unknown LOAMSS_PROFILE value")
	}
}

func TestProfile_Valid(t *testing.T) {
	if !Local.Valid() || !Cloud.Valid() {
		t.Error("known profiles should be valid")
	}
	if Profile("xyz").Valid() {
		t.Error("unknown profile should not be valid")
	}
}
