package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/config"
)

// daemonState reports whether the configured listen_addr accepts a TCP
// connection. The string form is what appears in the human-readable output;
// the JSON form is included for scripting.
//
// In v0.1 we only check connection acceptance — anything listening on the
// port satisfies "listening". Once the start command lands, status upgrades
// to an HTTP /healthz probe so it can distinguish loamss from another
// process that happens to occupy the port.
type daemonState struct {
	Listening bool   `json:"listening"`
	Reason    string `json:"reason,omitempty"`
}

// statusReport is the structured output of `loamss status`. The human
// renderer prints a subset; the --json path emits this verbatim.
type statusReport struct {
	ListenAddr     string      `json:"listen_addr"`
	DataDir        string      `json:"data_dir"`
	StorageAdapter string      `json:"storage_adapter"`
	MemoryAdapter  string      `json:"memory_adapter"`
	ModelAdapters  []string    `json:"model_adapters"`
	Daemon         daemonState `json:"daemon"`
}

var (
	statusJSON    bool
	statusTimeout time.Duration
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report runtime configuration and daemon state",
	Long: `Show what loamss is configured to run as, and whether the daemon
appears to be running.

In v0.1 the daemon-state check is a TCP probe of the configured
listen_addr — anything listening counts as "running". Once 'loamss
start' lands, this upgrades to an HTTP /healthz probe so we can
positively identify the loamss runtime (and report its version)
rather than just any process on that port.`,
	Args: cobra.NoArgs,
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, _ []string) error {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return fmt.Errorf("no config attached to context (programming error in the CLI wiring)")
	}

	r := buildStatusReport(cfg, statusTimeout)

	if statusJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	return renderStatus(cmd.OutOrStdout(), r)
}

// buildStatusReport gathers the report. Separated from the cobra entry
// point so tests can exercise the logic directly without a command tree.
func buildStatusReport(cfg *config.Config, timeout time.Duration) *statusReport {
	r := &statusReport{
		ListenAddr:     cfg.Runtime.ListenAddr,
		DataDir:        cfg.Runtime.DataDir,
		StorageAdapter: cfg.Storage.Adapter,
		MemoryAdapter:  cfg.Memory.Adapter,
	}
	for _, m := range cfg.Models {
		r.ModelAdapters = append(r.ModelAdapters, m.Adapter)
	}
	r.Daemon = probeDaemon(cfg.Runtime.ListenAddr, timeout)
	return r
}

// probeDaemon TCP-dials the listen address with a bounded timeout.
// We don't keep the connection open; we only care whether the bind succeeds.
func probeDaemon(addr string, timeout time.Duration) daemonState {
	if addr == "" {
		return daemonState{Listening: false, Reason: "no listen_addr configured"}
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err == nil {
		_ = conn.Close()
		return daemonState{Listening: true}
	}

	// Categorize the error for a more useful human message. We avoid
	// reaching deep into syscall error codes — net's error messages are
	// already reasonably clear, just verbose.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return daemonState{Listening: false, Reason: "connection refused"}
	case errors.Is(err, os.ErrDeadlineExceeded) || strings.Contains(msg, "i/o timeout"):
		return daemonState{Listening: false, Reason: "timed out (address may be unreachable)"}
	default:
		return daemonState{Listening: false, Reason: msg}
	}
}

func renderStatus(w io.Writer, r *statusReport) error {
	var b strings.Builder

	b.WriteString("Loamss runtime status\n\n")

	models := strings.Join(r.ModelAdapters, ", ")
	if models == "" {
		models = "(none configured)"
	}

	fmt.Fprintf(&b, "  Listen address:    %s\n", r.ListenAddr)
	fmt.Fprintf(&b, "  Data directory:    %s\n", r.DataDir)
	fmt.Fprintf(&b, "  Storage adapter:   %s\n", r.StorageAdapter)
	fmt.Fprintf(&b, "  Memory adapter:    %s\n", r.MemoryAdapter)
	fmt.Fprintf(&b, "  Models:            %s\n\n", models)

	if r.Daemon.Listening {
		fmt.Fprintf(&b, "  Daemon state:      running (process listening on %s)\n", r.ListenAddr)
	} else {
		reason := r.Daemon.Reason
		if reason == "" {
			reason = "not reachable"
		}
		fmt.Fprintf(&b, "  Daemon state:      not running (%s)\n", reason)
		b.WriteString("                     Run `loamss start` once start lands.\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "output as JSON")
	statusCmd.Flags().DurationVar(&statusTimeout, "timeout", time.Second,
		"how long to wait for the daemon TCP probe")
	rootCmd.AddCommand(statusCmd)
}
