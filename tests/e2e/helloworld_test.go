//go:build e2e
// +build e2e

package e2e

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHelloWorldField exercises the full apply -> ping -> destroy cycle.
// Requires: docker daemon running, root (for netns/veth).
func TestHelloWorldField(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	hclPath := filepath.Join(repoRoot, "examples/hello-world/field.sysbox.hcl")
	statePath := filepath.Join(repoRoot, "runs/e2e-helloworld/state.json")
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

	forceCleanup(t, statePath, "sysbox-node_a", "sysbox-node_b")
	t.Cleanup(func() { _, _ = sysbox("destroy") })

	out, err = sysbox("init")
	require.NoError(t, err, "init: %s", out)

	out, err = sysbox("apply")
	require.NoError(t, err, "apply: %s", out)
	require.Contains(t, string(out), "Apply complete")
	require.Contains(t, string(out), "4 to add")

	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_network.lan")
	require.Contains(t, string(out), "sysbox_node.node_a")
	require.Contains(t, string(out), "sysbox_node.node_b")

	ping := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.99.20")
	pingOut, err := ping.CombinedOutput()
	require.NoError(t, err, "ping failed: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets received")

	out, err = sysbox("destroy")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")

	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "(no resources)")
}
