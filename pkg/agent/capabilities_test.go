package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// fakeSubstrate is a minimal Node implementation for capability derivation
// tests. It embeds BaseSubstrate for the optional interface methods and only
// implements the ones with no sensible default.
type fakeSubstrate struct {
	substrate.BaseSubstrate
	caps   substrate.Capabilities
	checks []substrate.PreflightCheck
}

func (f *fakeSubstrate) Capabilities() substrate.Capabilities { return f.caps }
func (f *fakeSubstrate) PreflightChecks(bool) []substrate.PreflightCheck {
	return f.checks
}
func (f *fakeSubstrate) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}
func (f *fakeSubstrate) StartNode(context.Context, substrate.NodeHandle) error { return nil }
func (f *fakeSubstrate) StopNode(context.Context, substrate.NodeHandle) error  { return nil }
func (f *fakeSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error {
	return nil
}
func (f *fakeSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func newTestRegistry(t *testing.T) *driver.Registry {
	t.Helper()
	reg := driver.NewRegistry()
	require.NoError(t, reg.Register(driver.Descriptor{
		Name:    "docker",
		Version: "1",
		Node: &fakeSubstrate{
			caps: substrate.Capabilities{SharedKernel: true, NICKinds: []string{substrate.NICKindVeth}},
		},
	}))
	require.NoError(t, reg.Register(driver.Descriptor{
		Name:    "firecracker",
		Version: "1",
		Node: &fakeSubstrate{
			caps:   substrate.Capabilities{NICKinds: []string{substrate.NICKindTap}},
			checks: []substrate.PreflightCheck{{Name: "kvm_device", OK: false, Severity: "error"}},
		},
	}))
	return reg
}

func TestDeriveCapabilitiesFrom(t *testing.T) {
	reg := newTestRegistry(t)

	// A registered substrate pulls its NIC kind automatically.
	got := deriveCapabilitiesFrom(reg, []string{"docker"})
	require.ElementsMatch(t, []string{"docker", "veth", "network"}, got)

	// An unregistered substrate is claimed by name only, never guessed at.
	got = deriveCapabilitiesFrom(reg, []string{"unknown"})
	require.ElementsMatch(t, []string{"unknown", "network"}, got)
}

func TestDetectCapabilitiesFrom(t *testing.T) {
	reg := newTestRegistry(t)

	caps, detection := detectCapabilitiesFrom(reg)

	// firecracker fails detection (kvm missing) so only docker is claimed.
	require.ElementsMatch(t, []string{"docker", "veth", "network"}, caps)
	require.Equal(t, "ok", detection["docker"])
	require.Equal(t, "no-kvm_device", detection["firecracker"])
}

func TestDetectExplicitReferenceFrom(t *testing.T) {
	reg := newTestRegistry(t)

	// Explicit declaration of a substrate whose detection failed: the claim
	// stands but the discrepancy is visible.
	detection := detectExplicitReferenceFrom(reg, []string{"firecracker"})
	require.Equal(t, "claimed-but-no-kvm_device", detection["firecracker"])

	// An explicit substrate that detects cleanly is just "ok".
	detection = detectExplicitReferenceFrom(reg, []string{"docker"})
	require.Equal(t, "ok", detection["docker"])
}
