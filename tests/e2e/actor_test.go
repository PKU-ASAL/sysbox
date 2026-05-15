//go:build e2e
// +build e2e

// actor_test.go verifies that sysbox_actor brings up an opencode ACP
// server inside a container and the HTTP endpoint is reachable from
// the host after apply.
//
// What it tests:
//   1. sysbox_actor (position=internal) starts opencode serve inside a node.
//   2. The actor's acp_url is written to state at apply time.
//   3. The ACP HTTP endpoint responds (any status = server is listening).
//   4. POST /session creates a session object (proves the API is functional
//      at the transport level — no LLM call is made, so no API key needed).
//
// What it does NOT test:
//   - Sending a prompt through ACP (needs a valid LLM API key, minutes to run).
//   - Sensor / tracee / monitor integration (separate concern).
//
// Prerequisites:
//   - Docker daemon running.
//   - sysbox-attacker:latest image built (see examples/three-nodes/Dockerfile.attacker-opencode).
//   - Root (netns + netlink).
//
// Run: go test -tags e2e -v -run TestActorACP ./tests/e2e/...
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const actorHCL = `
substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "uplink" {
  cidr = "172.30.0.0/24"
  nat  = true
}

resource "sysbox_image" "attacker" {
  substrate  = substrate.docker.light
  docker_ref = "sysbox-attacker:latest"
}

resource "sysbox_node" "attack" {
  image     = sysbox_image.attacker.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.uplink.id
    ip      = "172.30.0.10/24"
  }
}

resource "sysbox_actor" "red" {
  position = "internal"
  node     = sysbox_node.attack.id
  command  = ["opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"]
  port     = 4096

  depends_on = ["sysbox_node.attack"]
}
`

// TestActorACP verifies that the opencode ACP endpoint comes up after apply
// and can serve basic HTTP requests from the host.
func TestActorACP(t *testing.T) {
	// This test only uses NAT (Docker bridge) networks, so it does NOT
	// require root. Skip only if Docker itself is unreachable.
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH")
	}

	// Ensure the attacker image exists.
	out, err := exec.Command("docker", "image", "inspect", "sysbox-attacker:latest", "--format", "{{.Id}}").CombinedOutput()
	if err != nil {
		t.Skipf("sysbox-attacker:latest not found; build with: docker build -t sysbox-attacker:latest -f examples/three-nodes/Dockerfile.attacker-opencode examples/three-nodes/")
	}
	_ = out

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	tmpDir := t.TempDir()
	hclPath := filepath.Join(tmpDir, "actor.sysbox.hcl")
	require.NoError(t, os.WriteFile(hclPath, []byte(actorHCL), 0o644))

	statePath := filepath.Join(repoRoot, "runs/e2e-actor/state.json")
	binPath := filepath.Join(repoRoot, "bin/sysbox")

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	buildCmd.Dir = repoRoot
	out, err = buildCmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(binPath, append(
			[]string{"--file", hclPath, "--state", statePath}, args...,
		)...)
		cmd.Dir = repoRoot
		return cmd.CombinedOutput()
	}

	forceCleanup(t, statePath, "sysbox-attack")
	t.Cleanup(func() { run("destroy", "--auto-approve") }) //nolint:errcheck

	// ── Apply ────────────────────────────────────────────────────────────────

	out, err = run("apply", "--auto-approve")
	require.NoError(t, err, "apply: %s", out)
	require.Contains(t, string(out), "Apply complete")
	require.Contains(t, string(out), "actor red started")

	// ── Read actor state ────────────────────────────────────────────────────

	out, err = run("state", "show", "sysbox_actor.red")
	require.NoError(t, err, "state show actor: %s", out)

	var stateEntry struct {
		Instance map[string]any `json:"instance"`
	}
	require.NoError(t, json.Unmarshal(out, &stateEntry))

	acpURL, _ := stateEntry.Instance["acp_url"].(string)
	require.NotEmpty(t, acpURL, "actor must have acp_url in state")
	t.Logf("ACP URL: %s", acpURL)

	// ── Wait for ACP to be reachable ────────────────────────────────────────

	client := &http.Client{Timeout: 5 * time.Second}
	require.Eventually(t, func() bool {
		resp, err := client.Get(acpURL + "/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true // any HTTP status = server is listening
	}, 30*time.Second, 500*time.Millisecond, "ACP endpoint never became reachable at %s", acpURL)

	// ── Create a session (proves the API is functional) ─────────────────────

	sessionResp, err := postJSON(client, acpURL+"/session", map[string]any{})
	require.NoError(t, err, "POST /session failed: %v", err)
	t.Logf("Session response: %s", string(sessionResp))

	var session map[string]any
	require.NoError(t, json.Unmarshal(sessionResp, &session))
	sessionID, _ := session["id"].(string)
	require.NotEmpty(t, sessionID, "/session must return an id")
	t.Logf("Session created: %s", sessionID)

	// ── Destroy ─────────────────────────────────────────────────────────────

	out, err = run("destroy", "--auto-approve")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")

	// Verify the ACP endpoint is gone after destroy.
	require.Eventually(t, func() bool {
		_, err := client.Get(acpURL + "/")
		return err != nil // connection refused = good
	}, 15*time.Second, 500*time.Millisecond, "ACP endpoint still reachable after destroy")
}

// postJSON sends a POST with a JSON body and returns the response body.
func postJSON(client *http.Client, url string, body map[string]any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(url, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("POST %s returned %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
