package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/config"
)

// Check statuses. Plain strings rather than an enum so JSON output is
// readable without a custom MarshalJSON.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type checkResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type doctorReport struct {
	Results []checkResult `json:"checks"`
	Counts  struct {
		OK   int `json:"ok"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"counts"`
}

func (r *doctorReport) record(res checkResult) {
	r.Results = append(r.Results, res)
	switch res.Status {
	case statusOK:
		r.Counts.OK++
	case statusWarn:
		r.Counts.Warn++
	case statusFail:
		r.Counts.Fail++
	}
}

var doctorJSON bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check the runtime's configuration and environment",
	Long: `Run a series of read-only health checks against the resolved
configuration: data directory writability, listen address validity,
adapter id shapes, and whether referenced model API keys are present
in the environment.

Doctor never modifies state. It reports what is wrong (or merely
worth noting) and exits with a non-zero code if any check failed.
Warnings do not affect the exit code.

Pass --json for machine-readable output suitable for scripting.`,
	Args: cobra.NoArgs,
	RunE: runDoctor,
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return fmt.Errorf("no config attached to context (programming error in the CLI wiring)")
	}

	r := runChecks(cfg)

	if doctorJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else if err := renderReport(cmd.OutOrStdout(), r); err != nil {
		return err
	}

	if r.Counts.Fail > 0 {
		// Return a sentinel error so the process exits non-zero, but suppress
		// cobra's "Error:" line (everything's already in the report).
		cmd.SilenceErrors = true
		return errDoctorFailed
	}
	return nil
}

// errDoctorFailed is the sentinel used to signal a non-zero exit without
// adding noise to the already-printed report.
var errDoctorFailed = fmt.Errorf("one or more checks failed")

// runChecks orchestrates each individual check and produces a report.
func runChecks(cfg *config.Config) *doctorReport {
	r := &doctorReport{}
	r.record(checkConfigSource(cfg))
	r.record(checkDataDir(cfg.Runtime.DataDir))
	r.record(checkListenAddr(cfg.Runtime.ListenAddr))
	r.record(checkAdapterID("Storage adapter", cfg.Storage.Adapter, "storage"))
	r.record(checkAdapterID("Memory adapter", cfg.Memory.Adapter, "memory"))
	for i, m := range cfg.Models {
		r.record(checkModelAdapter(i, m))
	}
	return r
}

// --- individual checks --------------------------------------------------

func checkConfigSource(cfg *config.Config) checkResult {
	path := filepath.Join(cfg.Runtime.DataDir, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		return checkResult{
			Name:    "Config source",
			Status:  statusOK,
			Message: path,
		}
	}
	return checkResult{
		Name:    "Config source",
		Status:  statusWarn,
		Message: fmt.Sprintf("no config.yaml at %s — using defaults (run `loamss init`)", path),
	}
}

func checkDataDir(dir string) checkResult {
	stat, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return checkResult{
				Name:    "Data directory",
				Status:  statusWarn,
				Message: fmt.Sprintf("%s does not exist (run `loamss init`)", dir),
			}
		}
		return checkResult{
			Name:    "Data directory",
			Status:  statusFail,
			Message: fmt.Sprintf("%s: %v", dir, err),
		}
	}
	if !stat.IsDir() {
		return checkResult{
			Name:    "Data directory",
			Status:  statusFail,
			Message: fmt.Sprintf("%s exists but is not a directory", dir),
		}
	}
	// Confirm writability by creating and immediately removing a temp file.
	f, err := os.CreateTemp(dir, ".loamss-doctor-*")
	if err != nil {
		return checkResult{
			Name:    "Data directory",
			Status:  statusFail,
			Message: fmt.Sprintf("%s exists but is not writable: %v", dir, err),
		}
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return checkResult{
		Name:    "Data directory",
		Status:  statusOK,
		Message: fmt.Sprintf("%s (writable)", dir),
	}
}

func checkListenAddr(addr string) checkResult {
	const name = "Listen address"

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return checkResult{
			Name:    name,
			Status:  statusFail,
			Message: fmt.Sprintf("%q is not a valid host:port: %v", addr, err),
		}
	}
	// SplitHostPort allows non-numeric ports (it splits on the last colon
	// and doesn't validate). We require an integer in [1, 65535].
	port, perr := strconv.ParseUint(portStr, 10, 16)
	if perr != nil || port == 0 {
		return checkResult{
			Name:    name,
			Status:  statusFail,
			Message: fmt.Sprintf("%q has no usable port (must be 1-65535)", addr),
		}
	}
	msg := fmt.Sprintf("%s — bound to host %q port %d", addr, host, port)

	// Warn if listen address is external rather than loopback. The runtime
	// defaults to localhost; binding externally is intentional but worth
	// surfacing because it changes the threat model.
	if host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return checkResult{
			Name:    name,
			Status:  statusWarn,
			Message: msg + " (non-loopback — runtime will be reachable from the network)",
		}
	}
	return checkResult{
		Name:    name,
		Status:  statusOK,
		Message: msg,
	}
}

func checkAdapterID(label, id, namespace string) checkResult {
	prefix := namespace + ":"
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) {
		return checkResult{
			Name:    label,
			Status:  statusFail,
			Message: fmt.Sprintf("%q has bad shape (expected %s<name>)", id, prefix),
		}
	}
	// The runtime cannot yet probe whether the adapter is actually registered
	// — no adapter registry exists in v0.1. Shape check is the most we can do
	// for now.
	return checkResult{
		Name:    label,
		Status:  statusOK,
		Message: id,
	}
}

func checkModelAdapter(idx int, m config.AdapterConfig) checkResult {
	name := fmt.Sprintf("Model adapter[%d]", idx)

	// First, the shape check.
	res := checkAdapterID(name, m.Adapter, "model")
	if res.Status != statusOK {
		return res
	}

	// If the adapter references an env-supplied credential, surface whether
	// that env var is actually populated. Adapters declare this via the
	// "api_key_env" key by convention.
	if v, ok := m.Config["api_key_env"].(string); ok && v != "" {
		if os.Getenv(v) == "" {
			return checkResult{
				Name:    name,
				Status:  statusWarn,
				Message: fmt.Sprintf("%s — env var %s is not set; adapter init will fail", m.Adapter, v),
			}
		}
		return checkResult{
			Name:    name,
			Status:  statusOK,
			Message: fmt.Sprintf("%s (api_key_env=%s present)", m.Adapter, v),
		}
	}
	return res
}

// --- rendering ----------------------------------------------------------

func renderReport(w io.Writer, r *doctorReport) error {
	var b strings.Builder

	b.WriteString("Loamss runtime health check\n\n")
	for _, c := range r.Results {
		b.WriteString("  ")
		b.WriteString(symbolFor(c.Status))
		b.WriteByte(' ')
		// Left-pad the label to a consistent width so the messages line up.
		const labelWidth = 18
		b.WriteString(c.Name)
		b.WriteString(":")
		pad := labelWidth - (len(c.Name) + 1)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(c.Message)
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%d ok, %d warn, %d fail\n",
		r.Counts.OK, r.Counts.Warn, r.Counts.Fail)

	_, err := io.WriteString(w, b.String())
	return err
}

func symbolFor(status string) string {
	switch status {
	case statusOK:
		return "✓"
	case statusWarn:
		return "⚠"
	case statusFail:
		return "✗"
	default:
		return "?"
	}
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "output report as JSON")
	rootCmd.AddCommand(doctorCmd)
}
