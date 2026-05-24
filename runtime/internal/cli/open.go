package cli

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/config"
)

// `loamss open` is the "I just installed this thing, now what?"
// command. It launches the user's browser at the runtime's URL so
// the wizard or dashboard appears without anyone having to remember
// the port number.
//
// The flow is intentionally conservative:
//
//  1. Resolve the URL from the configured listen_addr (defaults
//     to 127.0.0.1:7777). 0.0.0.0 / :: bindings are rewritten to
//     127.0.0.1 because that's what a browser on the same host
//     actually wants — opening "http://0.0.0.0:7777" works on
//     Linux but not on macOS, and a network address would expose
//     the URL to the wider network even when the user is local.
//
//  2. Probe /healthz with a short timeout. If unreachable, print a
//     hint ("run loamss start in another terminal") and exit with
//     a non-zero code so scripts notice. We still print the URL
//     so the user can copy/paste it.
//
//  3. Hand the URL to the OS opener. macOS: `open`. Linux:
//     `xdg-open`. Windows: `cmd /c start`. Any of these can fail
//     (no GUI, no $DISPLAY, locked-down sandbox) — when they do,
//     we still printed the URL above so the user has a path
//     forward.
//
// Refusing to silently swallow errors here is deliberate. This
// command runs once per install, and "nothing happened" is the
// confusing case we want to avoid.

var (
	openSkipProbe bool
	openNoLaunch  bool
)

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the Loamss console in your browser",
	Long: `Launch the system browser at the running runtime's URL.

The URL is derived from the configured listen_addr (default
127.0.0.1:7777). Bindings on 0.0.0.0 or :: are rewritten to
127.0.0.1 — the loopback address is what a same-host browser
actually wants.

If the daemon isn't listening, the command prints the URL anyway
and exits non-zero. The URL is useful even when the daemon's down
(you might be about to start it in another terminal).`,
	Args: cobra.NoArgs,
	RunE: runOpen,
}

func runOpen(cmd *cobra.Command, _ []string) error {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return fmt.Errorf("no config attached to context (programming error in the CLI wiring)")
	}

	url := consoleURLFromListenAddr(cfg.Runtime.ListenAddr)
	out := cmd.OutOrStdout()

	// Always print the URL first so the user has something useful
	// regardless of what happens next.
	_, _ = fmt.Fprintf(out, "Console URL: %s\n", url)

	if !openSkipProbe {
		if err := probeConsole(url, 750*time.Millisecond); err != nil {
			_, _ = fmt.Fprintf(out, "\nDaemon not reachable: %s\n", err)
			_, _ = fmt.Fprintln(out, "Start the runtime first (`loamss start`) and re-run `loamss open`.")
			return fmt.Errorf("daemon not reachable at %s", url)
		}
	}

	if openNoLaunch {
		return nil
	}

	if err := launchBrowser(url); err != nil {
		// We already printed the URL. A failure here means the
		// user just opens it themselves — annoying, not fatal.
		_, _ = fmt.Fprintf(out, "\nCouldn't launch browser automatically: %s\n", err)
		_, _ = fmt.Fprintln(out, "Copy the URL above into your browser.")
		return nil
	}
	return nil
}

// consoleURLFromListenAddr turns a configured listen_addr into a
// URL the user's browser can actually visit. The translation rules:
//
//   - "0.0.0.0" / "[::]" / empty host → "127.0.0.1" (loopback is
//     what a same-host browser wants; the wildcard bind exposes
//     the port to the network, which we don't want to encode into
//     the URL we print).
//   - Otherwise the host carries through unchanged. Users who set
//     a hostname in listen_addr presumably have a reason.
//   - Missing port (just a host) → ":7777" (the default).
func consoleURLFromListenAddr(addr string) string {
	if addr == "" {
		return "http://127.0.0.1:7777/"
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a host:port pair; assume it's just a host and append
		// the default port. SplitHostPort errors on the missing-port
		// form ("127.0.0.1") so this is the common branch for
		// hostnames without ports.
		host = addr
		port = "7777"
	}

	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	// IPv6 addresses need brackets in the URL. SplitHostPort
	// already removes them; SplitHostPort -> JoinHostPort would
	// also work, but we want to allow the empty-host->loopback
	// rewrite above to take effect first.
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s/", host, port)
}

// probeConsole hits /healthz on the resolved URL with a small
// timeout. We use the standard HTTP client (no shared transport
// pool — this runs once per invocation), and treat any non-2xx
// or transport error as "not reachable."
func probeConsole(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/healthz")
	if err != nil {
		// Most likely "connection refused" — daemon not running.
		// We don't unwrap further; the message is human-readable.
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected HTTP %d from %s/healthz", resp.StatusCode, strings.TrimRight(url, "/"))
	}
	return nil
}

// launchBrowser hands the URL to the OS-native opener. The set of
// platforms we support matches Go's GOOS values that have meaningful
// desktop browsers; anything else returns a "not supported" error.
//
// We deliberately don't capture stdout/stderr from the opener:
// `xdg-open` in particular happily forks a child and exits 0 even
// when the child can't display anything. Detecting that reliably
// would mean shipping a per-distro DBus probe; far past what this
// command needs to do.
func launchBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		// Use cmd's start builtin. The empty first argument is the
		// window title — required by cmd /c start when the URL is
		// the only following positional.
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		return fmt.Errorf("no known browser opener for %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", cmd.Path, err)
	}
	// Don't Wait() — the opener forks off the browser and lingering
	// here would block the CLI.
	_ = cmd.Process.Release()
	return nil
}

func init() {
	openCmd.Flags().BoolVar(&openSkipProbe, "skip-probe", false,
		"don't probe /healthz before launching (useful in scripts that race the daemon startup)")
	openCmd.Flags().BoolVar(&openNoLaunch, "no-launch", false,
		"print the URL but don't launch a browser (useful in headless environments)")
	rootCmd.AddCommand(openCmd)
}
