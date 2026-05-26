package capsule

import "context"

// LifecycleHook fires whenever a capsule subprocess is started or
// stopped by the Host. The runtime's scheduler implements this hook
// to keep its ingestor-tick goroutines in lockstep with the
// capsules' actual running state.
//
// The hook is best-effort: errors returned by either method are
// logged but do not block the start/stop they're reacting to. A
// capsule whose lifecycle hook fails to register a schedule still
// runs — the user gets no scheduled syncs but the rest of the
// capsule keeps working. This matches the "one broken capsule
// shouldn't break the rest" theme already present in Host.Start.
//
// The hook lives behind an interface so the capsule package stays
// free of imports on source.Store / audit-shape concerns the
// scheduler needs. The concrete daemon scheduler lives in cli/.
type LifecycleHook interface {
	// OnCapsuleStarted fires after a capsule's subprocess has come
	// up + handshaked and its tools are mounted in the runtime
	// registry. The supplied Installed record is the live one from
	// the capsule store; the implementation may read Manifest.Roles,
	// Manifest.Ingestor, etc.
	OnCapsuleStarted(ctx context.Context, c Installed)

	// OnCapsuleStopped fires after a capsule's subprocess has been
	// stopped by StopOne or Stop. Implementations should release any
	// per-capsule state they were holding (timer goroutines, in-flight
	// scheduled invocations, …).
	OnCapsuleStopped(ctx context.Context, name string)
}

// nopLifecycleHook is the zero-cost default. Used when no real hook
// has been wired (tests, the bare Host).
type nopLifecycleHook struct{}

func (nopLifecycleHook) OnCapsuleStarted(_ context.Context, _ Installed) {}
func (nopLifecycleHook) OnCapsuleStopped(_ context.Context, _ string)    {}
