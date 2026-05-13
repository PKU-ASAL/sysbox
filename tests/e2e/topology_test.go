//go:build e2e
// +build e2e

// topology_test.go exercises the core infrastructure layer:
// apply → connectivity → drift detection → destroy.
//
// Run: go test -tags e2e -v ./tests/e2e/... (requires Docker + root)
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTwoNetworksTopology verifies that sysbox can:
//   - build a two-subnet topology with a router
//   - route packets across subnets
//   - detect and recover from node drift
//   - cleanly destroy all resources
func TestTwoNetworksTopology(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("topology tests require root (netns + netlink); run: make test-e2e")
	}

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	hclPath := filepath.Join(repoRoot, "examples/two-networks/field.sysbox.hcl")
	statePath := filepath.Join(repoRoot, "runs/e2e-topology/state.json")
	binPath := filepath.Join(repoRoot, "bin/sysbox")

	// Build binary from source so the test always uses the current code.
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	buildCmd.Dir = repoRoot
	out, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(binPath, append(
			[]string{"--file", hclPath, "--state", statePath}, args...,
		)...)
		cmd.Dir = repoRoot
		return cmd.CombinedOutput()
	}
	apply := func(extra ...string) ([]byte, error) {
		return run(append([]string{"apply", "--auto-approve"}, extra...)...)
	}

	forceCleanup(t, statePath, "sysbox-node_a", "sysbox-node_b", "sysbox-edge")
	t.Cleanup(func() { run("destroy", "--auto-approve") }) //nolint:errcheck

	// ── Apply ────────────────────────────────────────────────────────────────

	out, err = apply()
	require.NoError(t, err, "apply: %s", out)
	require.Contains(t, string(out), "Apply complete")

	out, err = run("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_router.edge")
	require.Contains(t, string(out), "sysbox_node.node_a")
	require.Contains(t, string(out), "sysbox_node.node_b")

	// ── Cross-subnet connectivity ─────────────────────────────────────────────

	pingOut, err := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20").CombinedOutput()
	require.NoError(t, err, "cross-network ping failed: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets received")

	// ── Drift detection: no-op ────────────────────────────────────────────────

	out, err = apply("--refresh")
	require.NoError(t, err, "apply --refresh (no-op): %s", out)
	require.Contains(t, string(out), "No changes")

	// ── Drift detection: re-create after manual delete ────────────────────────

	exec.Command("docker", "rm", "-f", "sysbox-node_a").Run() //nolint:errcheck

	out, err = apply("--refresh")
	require.NoError(t, err, "apply --refresh (drift recovery): %s", out)
	require.Contains(t, string(out), "re-creating sysbox_node.node_a")
	require.Contains(t, string(out), "Apply complete")

	pingOut, err = exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.2.20").CombinedOutput()
	require.NoError(t, err, "ping after drift recovery: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets received")

	// ── Destroy ───────────────────────────────────────────────────────────────

	out, err = run("destroy", "--auto-approve")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")
}
