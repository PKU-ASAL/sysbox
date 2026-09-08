package substrate

import "sort"

// CapabilityNames returns the capability names a substrate contributes to an
// agent capability list: the substrate name itself, its NIC kinds, and — when
// the substrate shares the host kernel (containers) — the "docker" marker.
//
// It is the single source of truth for "which capabilities does supporting a
// substrate imply". The scheduler (pkg/api/scheduler.go) and the agent
// (pkg/agent/identity.go) both derive from this, so the two can never drift on
// a NIC-kind name like "veth" or "tap" again.
func CapabilityNames(substrateName string, caps Capabilities) []string {
	set := map[string]bool{substrateName: true}
	if caps.SharedKernel {
		set["docker"] = true
	}
	for _, kind := range caps.NICKinds {
		set[kind] = true
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SupportedSubstrates partitions detection results into the substrates that are
// supported and the reasons the rest are not. A substrate is unsupported only
// when one of its checks fails with severity "error"; warning-level failures
// (for example firecracker's "jailer isolation not implemented") downgrade but
// do not disqualify.
//
// reasons maps an unsupported substrate to the name of its first failing error
// check (e.g. "kvm_device", "firecracker_bin"), which the agent turns into a
// visible label such as substrate.firecracker=no-firecracker_bin.
func SupportedSubstrates(checks map[string][]PreflightCheck) (supported []string, reasons map[string]string) {
	supported = make([]string, 0, len(checks))
	reasons = make(map[string]string)
	for name, cs := range checks {
		if reason := firstErrorCheck(cs); reason != "" {
			reasons[name] = reason
			continue
		}
		supported = append(supported, name)
	}
	sort.Strings(supported)
	return supported, reasons
}

// firstErrorCheck returns the name of the first check that failed with
// severity "error", or "" if none did.
func firstErrorCheck(checks []PreflightCheck) string {
	for _, c := range checks {
		if !c.OK && c.Severity == "error" {
			return c.Name
		}
	}
	return ""
}
