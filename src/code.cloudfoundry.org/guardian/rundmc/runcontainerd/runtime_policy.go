package runcontainerd

import (
	"fmt"

	"github.com/containerd/containerd/v2/plugins"
)

// RuntimePolicy governs which containerd runtime adaptations are active.
// It is resolved once at Guardian startup from the configured runtime type
// and carried through the container lifecycle without re-parsing the string.
//
// Only two policies are supported: StockRuntime (default runc-v2) and
// RunscRuntime (validated gVisor/runsc shim). Unsupported runtime types
// are rejected at startup with an actionable error. This prevents unknown
// runtimes from silently inheriting runsc-specific workarounds such as
// CREATED-task shim cleanup, grouped image-backed peas, or exec-based
// archive streaming.
type RuntimePolicy int

const (
	// StockRuntime is the default runc-v2 containerd runtime. Empty
	// configuration and explicit "io.containerd.runc.v2" both resolve
	// to this policy. All stock containerd/runc behavior is unchanged.
	//
	// Must remain iota (zero value): IsAlternative relies on != StockRuntime,
	// and error-path callers depend on the zero value being safe-by-default.
	StockRuntime RuntimePolicy = iota

	// RunscRuntime is the validated gVisor/runsc containerd shim
	// (io.containerd.runsc.v1). It selects:
	//   - exec-based archive streaming (ExecStreamer, not NstarStreamer)
	//   - containerd task-API metrics (ContainerdStatser, not runc events)
	//   - image-backed grouped peas (requires use_containerd_for_processes)
	//   - runsc-specific OCI task options (no NoNewKeyring, no UID/GID)
	//   - Windows spec section stripping (runsc rejects it)
	//   - CREATED-task shim cleanup (host-PID intervention for stuck shims)
	RunscRuntime

	// KataRuntime is the Kata Containers runtime-v2 shim
	// (io.containerd.kata.v2). Kata boots a VM per container at task create
	// and snapshots the network namespace at that point, so the container's
	// network must be configured before the task exists. It shares the
	// alternative-runtime adaptations that do not depend on gVisor internals:
	// exec-based streaming, containerd task-API metrics and CRI sandbox
	// annotations. EXPERIMENTAL (CFAR-1511 spike).
	KataRuntime
)

// KataRuntimeType is the containerd runtime-v2 shim name for Kata Containers.
const KataRuntimeType = "io.containerd.kata.v2"

// SupportedRuntimeType is the only non-stock runtime type validated for
// the adaptations in this package. Other alternative runtimes (e.g. Kata)
// are not supported and are rejected at startup.
const SupportedRuntimeType = "io.containerd.runsc.v1"

// StockRuntimeType is the containerd plugin ID for the stock runc-v2 shim,
// directly referencing the vendored constant to track upstream changes.
// Policy decisions should use the resolved RuntimePolicy, not this string.
const StockRuntimeType = plugins.RuntimeRuncV2

// ResolveRuntimePolicy maps a containerd runtime type string to a validated
// RuntimePolicy. Returns the policy and nil for supported types. Returns an
// error for unsupported alternative runtimes — the error message is intended
// for Guardian startup logs and operator diagnosis.
func ResolveRuntimePolicy(runtimeType string) (RuntimePolicy, error) {
	switch runtimeType {
	case "", StockRuntimeType:
		return StockRuntime, nil
	case SupportedRuntimeType:
		return RunscRuntime, nil
	case KataRuntimeType:
		return KataRuntime, nil
	default:
		return StockRuntime, fmt.Errorf(
			"unsupported containerd runtime type %q: only stock runc (%s), gVisor/runsc (%s) and Kata (%s) are supported; "+
				"configure an empty or supported runtime type",
			runtimeType, StockRuntimeType, SupportedRuntimeType, KataRuntimeType,
		)
	}
}

// NetworkBeforeCreate reports whether the container network namespace must be
// created and configured before the containerd task is created.
func (p RuntimePolicy) NetworkBeforeCreate() bool {
	return p == KataRuntime
}

// IsAlternative returns true when the policy selects a non-stock runtime.
// This replaces the scattered != "" && != RuntimeRuncV2 predicates that
// were duplicated across guardiancmd, nerd, and command_linux.
func (p RuntimePolicy) IsAlternative() bool {
	return p != StockRuntime
}

// String returns the policy name for logging and diagnostics.
func (p RuntimePolicy) String() string {
	switch p {
	case StockRuntime:
		return "stock"
	case RunscRuntime:
		return "runsc"
	case KataRuntime:
		return "kata"
	default:
		return fmt.Sprintf("RuntimePolicy(%d)", int(p))
	}
}
