package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/profile"
	"github.com/loamss/loamss/runtime/internal/server"
)

// envSetupToken is the operator-provided override for the auto-generated
// setup token. When set, the gate uses this value verbatim (and the
// daemon does NOT print a fresh token on startup, since the operator
// already knows what they configured).
//
// Useful for Infrastructure-as-Code: stamp the token into secret-manager,
// inject it via the Cloud Run deploy command, and every cold start serves
// the same gate without log-scraping a generated value.
const envSetupToken = "LOAMSS_SETUP_TOKEN"

// setupConsumedFilename is the sentinel file inside <data_dir> that
// records prior consumption. Hidden (leading dot) to keep `ls` output
// uncluttered, but no security depends on it — the file's existence is
// the entire signal.
const setupConsumedFilename = ".setup-consumed"

// resolveSetupTokenGate constructs the server's setup-token gate based
// on the resolved deployment profile, the data directory, and the
// LOAMSS_SETUP_TOKEN env var. Returns (nil, nil) when the gate should
// be inactive — laptop installs in the local profile with no override.
//
// Activation matrix:
//
//	profile=local,  LOAMSS_SETUP_TOKEN unset   → inactive (laptop)
//	profile=local,  LOAMSS_SETUP_TOKEN set     → active (operator opt-in)
//	profile=cloud,  LOAMSS_SETUP_TOKEN set     → active (token = env)
//	profile=cloud,  LOAMSS_SETUP_TOKEN unset   → active (token auto-gen)
//
// On the auto-gen path the token is logged at INFO once at startup so
// the operator can grab it from Cloud Run / Fly logs. On the env-var
// path the token is NEVER logged (the operator already has it).
func resolveSetupTokenGate(
	prof profile.Profile,
	dataDir string,
	engine *permission.Engine,
	logger *slog.Logger,
) (*server.SetupTokenGate, error) {
	envToken := os.Getenv(envSetupToken)
	if prof == profile.Local && envToken == "" {
		// Laptop default — no gate, no token print, no warning.
		return nil, nil
	}

	var (
		token  string
		origin string
	)
	switch {
	case envToken != "":
		token = envToken
		origin = "env " + envSetupToken
	default:
		// Auto-generated. Cloud profile, operator didn't supply a
		// token. Generate one and log it conspicuously so the
		// operator can grab it from log output.
		generated, err := server.GenerateSetupToken()
		if err != nil {
			return nil, fmt.Errorf("generating setup token: %w", err)
		}
		token = generated
		origin = "auto-generated"
	}

	gate, err := server.NewSetupTokenGate(server.SetupTokenOptions{
		Token:        token,
		Origin:       origin,
		ConsumedPath: filepath.Join(dataDir, setupConsumedFilename),
		Engine:       engine,
	})
	if err != nil {
		return nil, err
	}

	// Operator-facing startup log. Two distinct shapes so log
	// scrapers can grep cleanly:
	//
	//   already consumed → "setup token gate active; previously consumed"
	//                      (the operator finished init on a prior boot)
	//   first start      → "setup token gate active" + token value in
	//                      a separate banner line below
	//
	// We never log the token value when it came from the env var —
	// the operator already knows it, and printing it back into log
	// aggregation is a leak we can avoid for free.
	if gate.IsConsumed() {
		logger.Info("setup token gate active; previously consumed",
			"profile", prof,
			"origin", origin,
			"hint", "delete <data_dir>/"+setupConsumedFilename+" to re-open the wizard",
		)
		return gate, nil
	}

	logger.Info("setup token gate active",
		"profile", prof,
		"origin", origin,
	)
	if origin == "auto-generated" {
		// Stand-alone banner line, not slog k=v, so it's grep-friendly
		// in the Cloud Run / Fly log UI. The token is high entropy and
		// not sensitive to log retention beyond the first /console/init.
		fmt.Fprintf(os.Stderr, "\n  ↪  Setup token: %s\n     Provide it as Authorization: Bearer <token> on the first /console/init request.\n     The token is single-use; subsequent access requires a paired-client credential.\n\n", token)
	}
	return gate, nil
}
