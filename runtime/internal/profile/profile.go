// Package profile resolves the deployment profile Loamss is running
// under. The profile picks defaults for listener binding, auth gating,
// and (eventually) database backend selection — see the v0.2 plan doc
// and rfc-cloud-deployment.md.
//
// Two profiles today:
//
//   - Local — laptop install. 127.0.0.1 binding, SQLite, no setup-token
//     gate. This is what runs today; default unless something says
//     otherwise.
//
//   - Cloud — container install (Cloud Run / Fly / GKE / Render / etc.).
//     0.0.0.0:$PORT binding, Postgres-friendly, setup-token-gated
//     wizard. Auto-detected from the well-known env vars cloud
//     platforms inject.
//
// Resolution order (first non-empty wins):
//
//  1. Explicit config (cfg.Runtime.Profile)
//  2. LOAMSS_PROFILE env var
//  3. Auto-detection from cloud-platform env vars
//  4. Default: Local
//
// The Profile shape is a typed string so config-file values can be
// validated at load time. Unknown profile strings are an error.
package profile

import (
	"fmt"
	"os"
)

// Profile names a deployment shape.
type Profile string

const (
	// Local is the laptop / self-hosted single-machine install. The
	// runtime binds to 127.0.0.1, no auth front-door on the wizard
	// because the binding itself is the gate.
	Local Profile = "local"

	// Cloud is the container-on-public-URL install. Binds to
	// 0.0.0.0:$PORT, requires a setup token to complete the wizard,
	// expects a managed Postgres for runtime + audit state.
	Cloud Profile = "cloud"
)

// String makes Profile satisfy fmt.Stringer.
func (p Profile) String() string { return string(p) }

// Valid reports whether p is one of the known profile names.
func (p Profile) Valid() bool {
	switch p {
	case Local, Cloud:
		return true
	default:
		return false
	}
}

// Detection describes how a profile was chosen — used for the
// startup banner so the operator can see why a particular profile
// was selected.
type Detection struct {
	Profile Profile
	Source  Source
	// Detail is a short explanation: the env-var name that triggered
	// auto-detection, or "config" / "env LOAMSS_PROFILE" / "default".
	Detail string
}

// Source identifies which resolution step picked the profile.
type Source string

// Recognised Source values. Each names a distinct step in the
// resolution order described in this file's package comment; the
// chosen step is recorded in Detection.Source for diagnostic
// logging and for "loamss doctor" / "loamss status" output.
const (
	SourceConfig  Source = "config"
	SourceEnvVar  Source = "env"
	SourceDetect  Source = "detected"
	SourceDefault Source = "default"
)

// Resolve picks the active profile.
//
// configValue is whatever cfg.Runtime.Profile contained — empty string
// when the user didn't set it. envVar is the LOAMSS_PROFILE env value
// (passed in rather than read here so tests can drive the resolution
// without t.Setenv).
//
// Returns an error if any explicit value is non-empty but not a valid
// profile name.
func Resolve(configValue, envVar string, envLookup func(string) string) (Detection, error) {
	if configValue != "" {
		p := Profile(configValue)
		if !p.Valid() {
			return Detection{}, fmt.Errorf("profile: unknown config value %q (want %q or %q)", configValue, Local, Cloud)
		}
		return Detection{Profile: p, Source: SourceConfig, Detail: "config"}, nil
	}
	if envVar != "" {
		p := Profile(envVar)
		if !p.Valid() {
			return Detection{}, fmt.Errorf("profile: unknown LOAMSS_PROFILE value %q (want %q or %q)", envVar, Local, Cloud)
		}
		return Detection{Profile: p, Source: SourceEnvVar, Detail: "env LOAMSS_PROFILE"}, nil
	}
	if det := autodetect(envLookup); det != (Detection{}) {
		return det, nil
	}
	return Detection{Profile: Local, Source: SourceDefault, Detail: "default"}, nil
}

// autodetect looks for well-known env vars cloud platforms inject.
// Returns the zero Detection if nothing matches.
//
// The check order matters only for the diagnostic Detail string; in
// practice no host sets multiple of these.
var detectors = []struct {
	envVar   string
	platform string
}{
	{"K_SERVICE", "Cloud Run"},
	{"KUBERNETES_SERVICE_HOST", "Kubernetes (GKE / EKS / AKS)"},
	{"FLY_APP_NAME", "Fly.io"},
	{"RENDER", "Render"},
	{"RAILWAY_ENVIRONMENT", "Railway"},
}

func autodetect(envLookup func(string) string) Detection {
	for _, d := range detectors {
		if envLookup(d.envVar) != "" {
			return Detection{
				Profile: Cloud,
				Source:  SourceDetect,
				Detail:  fmt.Sprintf("%s detected via env %s", d.platform, d.envVar),
			}
		}
	}
	return Detection{}
}

// OSEnvLookup wraps os.Getenv to satisfy the envLookup signature in
// Resolve. Real callers use this; tests construct their own lookup.
func OSEnvLookup(name string) string { return os.Getenv(name) }
