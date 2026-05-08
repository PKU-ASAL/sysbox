//go:build e2e
// +build e2e

package e2e

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		cmd.Stdin = nil
		return cmd.CombinedOutput()
	}
	autoApprove := func(sub string, extra ...string) ([]byte, error) {
		return sysbox(append([]string{sub, "--auto-approve"}, extra...)...)
	}

	forceCleanup(t, statePath, "sysbox-node_a", "sysbox-node_b", "sysbox-edge")
	t.Cleanup(func() { autoApprove("destroy") })

	out, err = sysbox("init")
	require.NoError(t, err, "init: %s", out)

	out, err = autoApprove("apply")
	require.NoError(t, err, "apply: %s\n", out)
	require.Contains(t, string(out), "Apply complete")

	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_router.edge")
	require.Contains(t, string(out), "sysbox_node.node_a")
	require.Contains(t, string(out), "sysbox_node.node_b")

	ping := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20")
	pingOut, err := ping.CombinedOutput()
	require.NoError(t, err, "cross-network ping failed: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets received")

	// drift detection no-op.
	out, err = autoApprove("apply", "--refresh")
	require.NoError(t, err, "apply --refresh no-op: %s", out)
	require.Contains(t, string(out), "No changes")

	// drift detection re-create: kill node_a, verify re-creation.
	exec.Command("docker", "rm", "-f", "sysbox-node_a").Run()

	out, err = autoApprove("apply", "--refresh")
	require.NoError(t, err, "apply --refresh after drift: %s", out)
	require.Contains(t, string(out), "re-creating sysbox_node.node_a")
	require.Contains(t, string(out), "Apply complete")

	ping2 := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20")
	ping2Out, err := ping2.CombinedOutput()
	require.NoError(t, err, "ping after drift recovery: %s", ping2Out)
	require.Contains(t, string(ping2Out), "1 packets received")

	out, err = autoApprove("destroy")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")
}
