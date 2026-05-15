//go:build e2e
// +build e2e

// monitor_test.go exercises the sensor / monitor surface end-to-end:
// build a tiny topology that declares a sysbox_monitor, apply it,
// verify state lists the monitor resource, and verify the sensor
// status command surfaces a sane events directory.
//
// We do NOT spin up the full tracee sidecar here — that requires
// privileged kernel access beyond what most CI runners offer. The
// test stops at "sysbox apply records the monitor intent + sensor
// status can read the events directory the sink would create".

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const monitorHCL = `
substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "lab" {
  cidr = "10.99.0.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  docker_ref = "alpine:3.19"
}

resource "sysbox_node" "n1" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.lab.id
    ip      = "10.99.0.10/24"
  }
}

resource "sysbox_monitor" "lab" {
  backend = "tracee"
  nodes   = [sysbox_node.n1.id]
  events  = ["execve", "openat"]
}
`

// TestMonitorApplyAndState verifies that:
//   - sysbox_monitor is parsed, decoded and applied without errors.
//   - State persists nodes, events, and backend for the monitor resource.
//   - Destroy cleans the monitor entry along with the rest of the topology.
func TestMonitorApplyAndState(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("monitor e2e requires root (netns + netlink); run: make test-e2e")
	}

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	tmpDir := t.TempDir()
	hclPath := filepath.Join(tmpDir, "monitor.sysbox.hcl")
	require.NoError(t, os.WriteFile(hclPath, []byte(monitorHCL), 0o644))

	statePath := filepath.Join(repoRoot, "runs/e2e-monitor/state.json")
	binPath := filepath.Join(repoRoot, "bin/sysbox")

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

	forceCleanup(t, statePath, "sysbox-n1")
	t.Cleanup(func() { run("destroy", "--auto-approve") }) //nolint:errcheck

	out, err = run("apply", "--auto-approve")
	require.NoError(t, err, "apply: %s", out)
	require.Contains(t, string(out), "Apply complete")
	require.Contains(t, string(out), "monitor lab")

	out, err = run("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_monitor.lab")
	require.Contains(t, string(out), "sysbox_node.n1")

	// Inspect the persisted state file to ensure the monitor resource has
	// the shape sensor_cmd / monitorsTargets expects.
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var s struct {
		Resources []struct {
			Type     string         `json:"type"`
			Name     string         `json:"name"`
			Provider string         `json:"provider"`
			Instance map[string]any `json:"instance"`
		} `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(data, &s))

	var monitor *struct {
		Type     string         `json:"type"`
		Name     string         `json:"name"`
		Provider string         `json:"provider"`
		Instance map[string]any `json:"instance"`
	}
	for i := range s.Resources {
		if s.Resources[i].Type == "sysbox_monitor" && s.Resources[i].Name == "lab" {
			monitor = &s.Resources[i]
			break
		}
	}
	require.NotNil(t, monitor, "sysbox_monitor.lab missing from state")
	require.Equal(t, "tracee", monitor.Instance["backend"])

	nodes, ok := monitor.Instance["nodes"].([]any)
	require.True(t, ok, "nodes should be a JSON array, got %T", monitor.Instance["nodes"])
	require.Equal(t, []any{"n1"}, nodes)

	events, ok := monitor.Instance["events"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"execve", "openat"}, events)

	// Sensor status: events dir doesn't exist yet because we never started
	// the sensor; the command should still exit 0 and report that fact.
	out, err = run("sensor", "status")
	require.NoError(t, err, "sensor status: %s", out)
	require.Contains(t, string(out), "No events directory found")

	out, err = run("destroy", "--auto-approve")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")
}
