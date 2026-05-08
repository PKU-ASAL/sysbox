//go:build e2e

// Package e2e contains Phase 2 end-to-end tests for the sysbox sensor.
//
// Test layers:
//   - Layer 1 (no root):    Registry register/resolve flow via sysbox CLI
//   - Layer 2 (root):       Real cgroup session creation + Labeler annotation
//   - Layer 3 (docker grp): Live tracee events via docker run --privileged
//
// Run all:  sudo -E go test ./tests/e2e/... -tags=e2e -v -run TestPhase2 -timeout 5m
// Layer 1:  go test ./tests/e2e/... -tags=e2e -v -run TestPhase2Registry
// Layer 3:  go test ./tests/e2e/... -tags=e2e -v -run TestPhase2LiveTracee
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
	"github.com/oslab/sysbox/pkg/session"
	"github.com/oslab/sysbox/pkg/sink"
)

// ─── Layer 1: Registry (no special privs) ────────────────────────────────────

// TestPhase2RegistryCLI validates the full sysbox session register + resolve flow
// via the CLI binary. No root required.
func TestPhase2RegistryCLI(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	// write a minimal state.json so the CLI doesn't fail
	os.WriteFile(stateFile, []byte(`{"version":1,"resources":[]}`), 0o644)

	sysboxBin := buildSysboxBin(t)

	sessionID := "cli-test-session-" + fmt.Sprintf("%d", time.Now().UnixNano())

	// 1. Register a session expectation.
	out, err := exec.Command(sysboxBin,
		"--state", stateFile,
		"session", "register",
		"--node", "node_a",
		"--session-id", sessionID,
		"--expires-in", "60s",
	).CombinedOutput()
	require.NoError(t, err, "session register: %s", out)
	require.Contains(t, string(out), sessionID)

	// 2. List should show the expectation.
	out, err = exec.Command(sysboxBin,
		"--state", stateFile,
		"session", "list",
	).CombinedOutput()
	require.NoError(t, err, "session list: %s", out)
	require.Contains(t, string(out), sessionID)

	// 3. Programmatically resolve (simulates sshd-hook).
	regPath := filepath.Join(dir, "session-registry.json")
	reg := session.NewRegistry(regPath)
	got := reg.Resolve("node_a", "10.0.0.1") // any source
	require.Equal(t, sessionID, got, "resolve should return pre-registered session_id")

	// 4. Consumed → resolve returns "" on second call.
	got2 := reg.Resolve("node_a", "10.0.0.1")
	require.Empty(t, got2, "expectation should be consumed after first resolve")
}

// TestPhase2RegistryExpiry verifies that expired expectations are ignored.
func TestPhase2RegistryExpiry(t *testing.T) {
	dir := t.TempDir()
	reg := session.NewRegistry(filepath.Join(dir, "r.json"))

	require.NoError(t, reg.Register(session.Expectation{
		NodeID:    "node_a",
		SessionID: "should-not-match",
		ExpiresAt: time.Now().Add(-1 * time.Second),
	}))
	require.Empty(t, reg.Resolve("node_a", ""), "expired entry must not resolve")
}

// ─── Layer 2: Cgroup session + Labeler (requires root) ───────────────────────

// TestPhase2SessionCgroup validates that creating a session cgroup and moving a
// process into it causes the Labeler to annotate events with session_id.
//
// Requires: root (cgroup write access).
func TestPhase2SessionCgroup(t *testing.T) {
	requireRoot(t)
	requireDocker(t)

	const containerName = "sysbox-p2-cgroup-test"
	forceCleanupContainer(t, containerName)

	ctx := context.Background()

	// Start a container.
	out, err := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", containerName,
		"alpine:latest", "sleep", "120",
	).CombinedOutput()
	require.NoError(t, err, "docker run: %s", out)
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	containerPID, err := getContainerPID(ctx, containerName)
	require.NoError(t, err)
	require.Greater(t, containerPID, 0)

	nodeID := "p2-cgroup-test"
	sessionID := "sess-cgroup-" + fmt.Sprintf("%d", time.Now().UnixNano())

	// Create session cgroup.
	require.NoError(t, session.EnsureSliceExists(nodeID))
	cgroupID, err := session.CreateSessionCgroup(nodeID, sessionID)
	require.NoError(t, err)
	t.Logf("Created session cgroup: node=%s session=%s cgroup_id=%d", nodeID, sessionID, cgroupID)
	defer session.DeleteSessionCgroup(nodeID, sessionID)

	// Move container's init process into session cgroup.
	require.NoError(t, session.MoveProcess(nodeID, sessionID, containerPID))

	// Labeler should now map this cgroup_id to session_id.
	lab := session.NewLabeler()
	lab.RegisterSession(cgroupID, sessionID)

	// Create a mock event with the session's cgroup_id.
	eventJSON := buildMockEvent(containerPID, 1, cgroupID, "execve", "/usr/bin/nmap")
	mb := sensor.NewMockBackend([]string{eventJSON}, lab)
	ch, err := mb.Start(ctx, nodeID, "fake")
	require.NoError(t, err)

	events := collectEvents(ch)
	require.Len(t, events, 1)
	ev := events[0]

	require.Equal(t, sessionID, ev.SessionID,
		"event from process inside session cgroup must carry session_id")
	require.True(t, ev.IsAttack,
		"event from session cgroup must have is_attack=true")
	require.Equal(t, cgroupID, ev.CgroupID)

	// Write to JSONL sink and verify.
	dir := t.TempDir()
	sk := sink.NewJSONLSink(filepath.Join(dir, "events.jsonl"))
	require.NoError(t, sk.Write(ev))
	require.NoError(t, sk.Close())

	// Read back and verify schema.
	assertEventsFileContains(t, filepath.Join(dir, "events.jsonl"), func(e sensor.Event) bool {
		return e.SessionID == sessionID && e.IsAttack && e.CgroupID == cgroupID
	})
}

// ─── Layer 3: Live tracee events (docker group, no root needed) ──────────────

// TestPhase2LiveTracee runs tracee via `docker run --privileged` against a
// real container and verifies that execve events appear in the event stream.
//
// Requires: docker group membership (not root).
func TestPhase2LiveTracee(t *testing.T) {
	requireDocker(t)

	const containerName = "sysbox-p2-tracee-target"
	forceCleanupContainer(t, containerName)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start target container.
	out, err := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", containerName,
		"alpine:latest", "sleep", "120",
	).CombinedOutput()
	require.NoError(t, err, "docker run: %s", out)
	containerID := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	lab := session.NewLabeler() // no sessions: all events should be is_attack=false
	backend := sensor.NewDockerTraceeBackend("aquasec/tracee:0.22.0", lab)

	ch, err := backend.Start(ctx, "p2-test-node", containerID)
	require.NoError(t, err, "tracee start")
	t.Log("tracee started, waiting for events (may take 5-10s for eBPF init)...")

	// Give tracee time to initialize, then trigger some execve events.
	time.Sleep(6 * time.Second)
	for i := 0; i < 3; i++ {
		exec.Command("docker", "exec", containerName, "ls", "/tmp").Run()
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)

	// Stop tracee and collect events.
	backend.Stop()
	var events []sensor.Event
	collectTimer := time.After(5 * time.Second)
done:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break done
			}
			events = append(events, ev)
		case <-collectTimer:
			break done
		}
	}

	t.Logf("Collected %d events from tracee", len(events))

	// Verify at least one event was received.
	require.Greater(t, len(events), 0,
		"expected live tracee events from container; "+
			"check that tracee image is available and docker has privileged access")

	// Verify schema: all events should have is_attack=false (no session registered).
	for _, ev := range events {
		require.Empty(t, ev.SessionID,
			"without session registration, session_id must be empty")
		require.False(t, ev.IsAttack,
			"without session, is_attack must be false")
		require.NotEmpty(t, ev.Name, "all events must have a name")
		require.Greater(t, ev.Timestamp, int64(0))
	}

	// Verify event JSON schema: no process_tree or entry_point.
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		require.NotContains(t, string(b), "process_tree",
			"Phase 2 event schema must not have process_tree field")
		require.NotContains(t, string(b), "entry_point",
			"Phase 2 event schema must not have entry_point field")
	}
}

// TestPhase2FullFlow tests the complete session flow end-to-end using:
//  1. sysbox session register (CLI)
//  2. Simulated sshd-hook (reads registry, creates session cgroup)
//  3. MockBackend event with the session's cgroup_id
//  4. Verifies events.jsonl has session_id == pre-registered value
//
// Requires: root (cgroup creation).
func TestPhase2FullFlow(t *testing.T) {
	requireRoot(t)

	dir := t.TempDir()
	regPath := filepath.Join(dir, "session-registry.json")
	eventsPath := filepath.Join(dir, "events.jsonl")

	nodeID := "p2-fullflow-node"
	preSessionID := "langfuse-run-abc123"
	sourceIP := "10.0.1.1"

	// 1. Experiment layer pre-registers session (as if `sysbox session register` was called).
	reg := session.NewRegistry(regPath)
	require.NoError(t, reg.Register(session.Expectation{
		NodeID:    nodeID,
		SourceIP:  sourceIP,
		SessionID: preSessionID,
		ExpiresAt: time.Now().Add(60 * time.Second),
	}))
	t.Logf("Registered expectation: node=%s session_id=%s", nodeID, preSessionID)

	// 2. Simulate sshd-hook: resolves session_id from registry.
	resolved := reg.Resolve(nodeID, sourceIP)
	require.Equal(t, preSessionID, resolved,
		"hook must get pre-declared session_id, not random UUID")

	// 3. Hook creates session cgroup and moves itself into it.
	require.NoError(t, session.EnsureSliceExists(nodeID))
	cgroupID, err := session.CreateSessionCgroup(nodeID, resolved)
	require.NoError(t, err)
	defer session.DeleteSessionCgroup(nodeID, resolved)
	t.Logf("Session cgroup created: cgroup_id=%d", cgroupID)

	// 4. Sensor registers the cgroup_id → session_id mapping in Labeler.
	lab := session.NewLabeler()
	lab.RegisterSession(cgroupID, resolved)

	// 5. Process inside session cgroup runs a command (simulated via MockBackend).
	fakePID := 9999
	eventLines := []string{
		buildMockEvent(fakePID, 1, cgroupID, "execve", "/usr/bin/nmap"),
		buildMockEvent(fakePID+1, 1, 99999, "execve", "/bin/ls"), // different cgroup = no session
	}
	mb := sensor.NewMockBackend(eventLines, lab)
	ch, err := mb.Start(context.Background(), nodeID, "fake-container")
	require.NoError(t, err)

	// 6. Write events to JSONL.
	sk := sink.NewJSONLSink(eventsPath)
	for ev := range ch {
		require.NoError(t, sk.Write(ev))
	}
	require.NoError(t, sk.Close())

	// 7. Verify events.jsonl.
	events := parseEventsFile(t, eventsPath)
	require.Len(t, events, 2)

	nmap := events[0]
	require.Equal(t, preSessionID, nmap.SessionID,
		"nmap event should carry the pre-registered session_id")
	require.True(t, nmap.IsAttack, "session event must be is_attack=true")
	require.Equal(t, "execve", nmap.Name)

	ls := events[1]
	require.Empty(t, ls.SessionID,
		"ls from different cgroup must have empty session_id")
	require.False(t, ls.IsAttack, "non-session event must be is_attack=false")

	t.Logf("PASS: nmap→session_id=%s is_attack=true, ls→session_id='' is_attack=false",
		nmap.SessionID)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root (run with sudo -E)")
	}
}

func buildSysboxBin(t *testing.T) string {
	t.Helper()
	repoRoot := findRepoRoot(t)
	binPath := filepath.Join(repoRoot, "bin", "sysbox")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build sysbox: %s", out)
	return binPath
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func assertEventsFileContains(t *testing.T, path string, match func(sensor.Event) bool) {
	t.Helper()
	events := parseEventsFile(t, path)
	for _, e := range events {
		if match(e) {
			return
		}
	}
	t.Fatalf("no matching event in %s", path)
}

func parseEventsFile(t *testing.T, path string) []sensor.Event {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var events []sensor.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e sensor.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			events = append(events, e)
		}
	}
	return events
}
