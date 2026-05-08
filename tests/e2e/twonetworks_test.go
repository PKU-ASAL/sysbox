//go:build e2e
// +build e2e

package e2e

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTwoNetworksField verifies that a sysbox_router forwards packets between
// two isolated networks.
//
// Topology:
//   node_a (10.0.1.10/24, gw=10.0.1.254) <--net_a--> router <--net_b--> node_b (10.0.2.20/24, gw=10.0.2.254)
//
// Requires: docker daemon + root.
func TestTwoNetworksField(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	hclPath := filepath.Join(repoRoot, "examples/two-networks/field.sysbox.hcl")
	statePath := filepath.Join(repoRoot, "runs/e2e-twonetworks/state.json")
	binPath := filepath.Join(repoRoot, "bin/sysbox")

	build := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	sysbox := func(args ...string) ([]byte, error) {
		full := append([]string{"-f", hclPath, "--state", statePath}, args...)
		cmd := exec.Command(binPath, full...)
		cmd.Dir = repoRoot
		return cmd.CombinedOutput()
	}

	forceCleanup(t, statePath, "sysbox-node_a", "sysbox-node_b", "sysbox-edge")
	t.Cleanup(func() { _, _ = sysbox("destroy") })

	out, err = sysbox("init")
	require.NoError(t, err, "init: %s", out)

	out, err = sysbox("apply")
	require.NoError(t, err, "apply: %s\n", out)
	require.Contains(t, string(out), "Apply complete")

	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_router.edge")
	require.Contains(t, string(out), "sysbox_node.node_a")
	require.Contains(t, string(out), "sysbox_node.node_b")

	// node_a should reach node_b across the router.
	ping := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20")
	pingOut, err := ping.CombinedOutput()
	require.NoError(t, err, "cross-network ping failed: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets received")

	// drift detection (no-op): healthy field should report no changes.
	out, err = sysbox("apply", "--refresh")
	require.NoError(t, err, "apply --refresh no-op: %s", out)
	require.Contains(t, string(out), "No changes")

	// drift detection (re-create): manually kill node_a then apply --refresh.
	kill := exec.Command("docker", "rm", "-f", "sysbox-node_a")
	_, _ = kill.CombinedOutput()

	out, err = sysbox("apply", "--refresh")
	require.NoError(t, err, "apply --refresh after drift: %s", out)
	require.Contains(t, string(out), "re-creating sysbox_node.node_a")
	require.Contains(t, string(out), "Apply complete")

	// node_a should be reachable again after re-create.
	ping2 := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20")
	ping2Out, err := ping2.CombinedOutput()
	require.NoError(t, err, "ping after drift recovery: %s", ping2Out)
	require.Contains(t, string(ping2Out), "1 packets received")

	out, err = sysbox("destroy")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")
}
