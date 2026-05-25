package config

import (
	"reflect"
	"sort"
)

// `diff.go` — pure-function helpers that compare two Configs and
// report what changed.
//
// Used by the runtime to decide what needs a graceful restart vs
// what can be hot-swapped. The wizard reload flow reads the
// config file, builds a fresh Config, and asks Diff(old, new)
// what shifted; the answer drives the dashboard's "restart
// required" badge.
//
// Why a separate file: the diff logic touches every field group
// in Config, so keeping it next to the schema makes "I just added
// a field; do I need a diff?" obvious during review. The
// alternative — diffing inline in the reload handler — buries
// every new field in a series of `if old.X != new.X {}` calls
// inside the server package.

// DiffResult describes how two Configs differ, grouped by the
// runtime's hot-swap policy:
//
//   - HotSwapped: fields the daemon can adopt without a restart
//     (log level/format). Already applied by the reload pipeline.
//   - RestartRequired: fields whose change demands a `loamss
//     start` restart (storage adapter, memory adapter, models,
//     listen_addr, data_dir). The old value is still in effect.
//   - Unchanged: nothing differs.
//
// IsEmpty reports whether either bucket has at least one entry.
type DiffResult struct {
	HotSwapped      []FieldChange
	RestartRequired []FieldChange
}

// FieldChange is one schema-path differing between two configs.
// Path is dot-notation (e.g. "runtime.listen_addr"); From and To
// are the rendered values (we don't try to preserve original Go
// types — JSON-string is enough for the audit log and the
// dashboard).
type FieldChange struct {
	Path string `json:"path"`
	From any    `json:"from,omitempty"`
	To   any    `json:"to,omitempty"`
}

// Diff compares two Configs and bucket-classifies the differences.
// Both inputs may be nil; a nil input is treated as "no opinion
// on any field" and produces an empty result against another nil,
// or a full diff against a non-nil input.
//
// Field-by-field, not deep-equal — we want the human-meaningful
// path back, not just "they differ."
func Diff(oldCfg, newCfg *Config) DiffResult {
	if oldCfg == nil && newCfg == nil {
		return DiffResult{}
	}
	if oldCfg == nil {
		oldCfg = &Config{}
	}
	if newCfg == nil {
		newCfg = &Config{}
	}
	old, new := oldCfg, newCfg //nolint:revive // local aliases for body readability

	var hot, restart []FieldChange

	// --- Runtime block ---
	if old.Runtime.DataDir != new.Runtime.DataDir {
		restart = append(restart, FieldChange{
			Path: "runtime.data_dir",
			From: old.Runtime.DataDir,
			To:   new.Runtime.DataDir,
		})
	}
	if old.Runtime.ListenAddr != new.Runtime.ListenAddr {
		restart = append(restart, FieldChange{
			Path: "runtime.listen_addr",
			From: old.Runtime.ListenAddr,
			To:   new.Runtime.ListenAddr,
		})
	}

	// --- Storage ---
	// Adapter-id changes are restart-required (re-init holds open
	// file handles); config-map changes inside the same adapter
	// also require restart for now because we'd need to call
	// adapter.Init(newConfig) which most adapters don't expose
	// after first Init.
	if old.Storage.Adapter != new.Storage.Adapter {
		restart = append(restart, FieldChange{
			Path: "storage.adapter",
			From: old.Storage.Adapter,
			To:   new.Storage.Adapter,
		})
	} else if !reflect.DeepEqual(old.Storage.Config, new.Storage.Config) {
		restart = append(restart, FieldChange{
			Path: "storage.config",
			From: old.Storage.Config,
			To:   new.Storage.Config,
		})
	}

	// --- Memory ---
	if old.Memory.Adapter != new.Memory.Adapter {
		restart = append(restart, FieldChange{
			Path: "memory.adapter",
			From: old.Memory.Adapter,
			To:   new.Memory.Adapter,
		})
	} else if !reflect.DeepEqual(old.Memory.Config, new.Memory.Config) {
		restart = append(restart, FieldChange{
			Path: "memory.config",
			From: old.Memory.Config,
			To:   new.Memory.Config,
		})
	}

	// --- Models ---
	// Any model list change requires restart for v1 — the MCP tools
	// (memory.query, model.call) close over specific adapter
	// instances at startup. Future work: route through a swappable
	// ModelRouter; flagged in the docs.
	if !modelListsEqual(old.Models, new.Models) {
		restart = append(restart, FieldChange{
			Path: "models",
			From: adapterListSummary(old.Models),
			To:   adapterListSummary(new.Models),
		})
	}

	// --- Routing (not yet enforced anywhere) — track silently so a
	// future commit can light it up without changing this signature.
	if !routingListsEqual(old.Routing, new.Routing) {
		restart = append(restart, FieldChange{
			Path: "routing",
			From: routingListSummary(old.Routing),
			To:   routingListSummary(new.Routing),
		})
	}

	// --- Audit (hot-swap-able subset; full reconfigure deferred) ---
	if old.Audit.RedactionLevel != new.Audit.RedactionLevel {
		// We don't yet plumb redaction-level changes into the live
		// writer, so treat as restart-required until that
		// integration lands.
		restart = append(restart, FieldChange{
			Path: "audit.redaction_level",
			From: old.Audit.RedactionLevel,
			To:   new.Audit.RedactionLevel,
		})
	}
	if old.Audit.HotStoreMaxDays != new.Audit.HotStoreMaxDays {
		restart = append(restart, FieldChange{
			Path: "audit.hot_store_max_days",
			From: old.Audit.HotStoreMaxDays,
			To:   new.Audit.HotStoreMaxDays,
		})
	}
	if old.Audit.HotStoreMaxMB != new.Audit.HotStoreMaxMB {
		restart = append(restart, FieldChange{
			Path: "audit.hot_store_max_mb",
			From: old.Audit.HotStoreMaxMB,
			To:   new.Audit.HotStoreMaxMB,
		})
	}

	// --- Log (HOT-SWAPPABLE) ---
	// slog handlers are stateless; rebuilding one with new
	// level/format is a single allocation. The reload pipeline
	// swaps the daemon's logger pointer in place.
	if old.Log.Level != new.Log.Level {
		hot = append(hot, FieldChange{
			Path: "log.level",
			From: old.Log.Level,
			To:   new.Log.Level,
		})
	}
	if old.Log.Format != new.Log.Format {
		hot = append(hot, FieldChange{
			Path: "log.format",
			From: old.Log.Format,
			To:   new.Log.Format,
		})
	}

	return DiffResult{HotSwapped: hot, RestartRequired: restart}
}

// IsEmpty reports whether the diff has any differences. Used by
// the reload handler to skip the audit entry when nothing
// actually changed.
func (d DiffResult) IsEmpty() bool {
	return len(d.HotSwapped) == 0 && len(d.RestartRequired) == 0
}

// modelListsEqual compares two adapter slices by both adapter id
// and config map. Order matters — the router picks the first
// match, so reordering is semantically meaningful.
func modelListsEqual(a, b []AdapterConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Adapter != b[i].Adapter {
			return false
		}
		if !reflect.DeepEqual(a[i].Config, b[i].Config) {
			return false
		}
	}
	return true
}

func routingListsEqual(a, b []RoutingRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// adapterListSummary renders an adapter-config slice as a sorted
// list of "adapter_id" strings for the diff's human-readable
// before/after. We omit the per-adapter config maps from the
// summary — they're noisy and would dominate the dashboard's
// "what changed" tooltip.
func adapterListSummary(adapters []AdapterConfig) []string {
	out := make([]string, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a.Adapter)
	}
	sort.Strings(out)
	return out
}

func routingListSummary(rules []RoutingRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Task+"→"+r.Prefer)
	}
	sort.Strings(out)
	return out
}
