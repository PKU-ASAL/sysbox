//go:build e2e

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

// TestSensorSSHSession verifies that when a session cgroup is created and a
// process is moved into it, events labelled with that cgroup_id get
// session_id != "" and is_attack == true.
//
// This test exercises the full sensor pipeline using MockBackend (no tracee
// binary required). Real tracee integration is validated in production
// by TestSensorLiveTracee (requires tracee binary + root).
func TestSensorSSHSession(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	eventSink := sink.NewJSONLSink(eventsPath)
	defer eventSink.Close()

	lab := session.NewLabeler()
	nodeID := "test-node"
	sessionID := "sess-ssh-abc"

	// Simulate: sensor cgroup created + process moved in.
	// In real flow this is done by the controlServer handling the sshd-hook message.
	require.NoError(t, session.EnsureSliceExists(nodeID))
	cgroupID, err := session.CreateSessionCgroup(nodeID, sessionID)
	require.NoError(t, err)
	defer session.DeleteSessionCgroup(nodeID, sessionID)

	lab.RegisterSession(cgroupID, sessionID)

	// Build a mock event that has cgroup_id == the session cgroup.
	eventJSON := buildMockEvent(1234, 100, cgroupID, "execve", "/usr/bin/nmap")
	mb := sensor.NewMockBackend([]string{eventJSON}, lab)

	ch, err := mb.Start(context.Background(), nodeID, "fake-container")
	require.NoError(t, err)

	events := collectEvents(ch)
	require.Len(t, events, 1, "expected one event")

	ev := events[0]
	require.Equal(t, "execve", ev.Name)
	require.Equal(t, sessionID, ev.SessionID, "event should carry session_id")
	require.True(t, ev.IsAttack, "event should be is_attack=true when in session cgroup")
	require.Equal(t, cgroupID, ev.CgroupID)

	// Write events to sink and verify file.
	require.NoError(t, eventSink.Write(ev))
	require.NoError(t, eventSink.Close())
	assertEventInFile(t, eventsPath, func(e sensor.Event) bool {
		return e.SessionID == sessionID && e.Name == "execve"
	})
}

// TestSensorRegisteredSessionID verifies that when `sysbox session register`
// pre-declares a session, the sshd-hook resolves it and the labeler
// receives the pre-declared session_id (not a random UUID).
func TestSensorRegisteredSessionID(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "session-registry.json")

	preSessionID := "exp-langfuse-abc"
	nodeID := "test-node-b"

	// Register expectation.
	reg := session.NewRegistry(regPath)
	require.NoError(t, reg.Register(session.Expectation{
		NodeID:    nodeID,
		SourceIP:  "10.0.0.5",
		SessionID: preSessionID,
		ExpiresAt: time.Now().Add(60 * time.Second),
	}))

	// Simulate what sshd-hook does: resolve + create cgroup.
	resolved := reg.Resolve(nodeID, "10.0.0.5")
	require.Equal(t, preSessionID, resolved, "hook should get pre-declared session_id")

	lab := session.NewLabeler()
	require.NoError(t, session.EnsureSliceExists(nodeID))
	cgroupID, err := session.CreateSessionCgroup(nodeID, resolved)
	require.NoError(t, err)
	defer session.DeleteSessionCgroup(nodeID, resolved)

	lab.RegisterSession(cgroupID, resolved)

	// Simulate an event from inside that cgroup.
	eventJSON := buildMockEvent(2000, 200, cgroupID, "execve", "/usr/bin/whoami")
	mb := sensor.NewMockBackend([]string{eventJSON}, lab)
	ch, err := mb.Start(context.Background(), nodeID, "fake")
	require.NoError(t, err)

	events := collectEvents(ch)
	require.Len(t, events, 1)
	require.Equal(t, preSessionID, events[0].SessionID,
		"event session_id should be the pre-registered one, not a random UUID")

	// Once consumed, resolve returns "".
	resolved2 := reg.Resolve(nodeID, "10.0.0.5")
	require.Empty(t, resolved2, "expectation should be consumed after first resolve")
}

// TestSensorNonSessionEvents verifies the strict cgroup-only semantic:
// events from processes that are NOT in a session cgroup get
// session_id == "" and is_attack == false.
func TestSensorNonSessionEvents(t *testing.T) {
	lab := session.NewLabeler()
	// No sessions registered.

	// Event with cgroup_id 12345 (not a session cgroup).
	eventJSON := buildMockEvent(5678, 1, 12345, "execve", "/bin/ls")
	mb := sensor.NewMockBackend([]string{eventJSON}, lab)
	ch, err := mb.Start(context.Background(), "node-noauth", "fake")
	require.NoError(t, err)

	events := collectEvents(ch)
	require.Len(t, events, 1)
	ev := events[0]
	require.Empty(t, ev.SessionID,
		"non-session event should have empty session_id (no cgroup match)")
	require.False(t, ev.IsAttack,
		"non-session event should have is_attack=false (Phase 2 strict semantic)")

	// Also verify the event has no process_tree or entry_point fields
	// by marshaling and checking the JSON.
	data, err := json.Marshal(ev)
	require.NoError(t, err)
	require.NotContains(t, string(data), "process_tree",
		"event schema must not contain process_tree in Phase 2")
	require.NotContains(t, string(data), "entry_point",
		"event schema must not contain entry_point in Phase 2")
}

// TestSensorE2EDockerExec is a full integration test that starts a real Docker
// container, simulates a non-session process entry via docker exec, and
// asserts that the resulting (mock) events are correctly labelled.
// This test requires Docker and root.
func TestSensorE2EDockerExec(t *testing.T) {
	requireDocker(t)

	const containerName = "sysbox-sensor-e2e-test"
	forceCleanupContainer(t, containerName)

	ctx := context.Background()

	// Start a minimal container.
	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", containerName,
		"--cap-add", "NET_ADMIN",
		"alpine:latest", "sleep", "infinity")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker run: %s", string(out))
	containerID := strings.TrimSpace(string(out))
	defer func() {
		exec.Command("docker", "rm", "-f", containerName).Run()
	}()

	// Get container's cgroup_id to verify non-session labeling.
	containerPID, err := getContainerPID(ctx, containerName)
	require.NoError(t, err)
	cgroupID, err := session.CgroupIDFromProc(containerPID)
	require.NoError(t, err)

	lab := session.NewLabeler()
	// No session registered for this container's cgroup.

	// Simulate an exec event from inside the container.
	eventJSON := buildMockEvent(containerPID, 1, cgroupID, "execve", "/bin/ls")
	mb := sensor.NewMockBackend([]string{eventJSON}, lab)
	ch, err := mb.Start(ctx, containerName, containerID)
	require.NoError(t, err)

	events := collectEvents(ch)
	require.Len(t, events, 1)
	require.Empty(t, events[0].SessionID)
	require.False(t, events[0].IsAttack)
}

// --- helpers ---

func buildMockEvent(pid, ppid int, cgroupID uint64, eventName, arg0 string) string {
	m := map[string]any{
		"timestamp":           float64(time.Now().UnixNano()),
		"hostProcessId":       float64(pid),
		"hostParentProcessId": float64(ppid),
		"cgroupId":            float64(cgroupID),
		"processName":         eventName,
		"eventName":           eventName,
		"args": []any{
			map[string]any{"name": "pathname", "type": "const char*", "value": arg0},
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func collectEvents(ch <-chan sensor.Event) []sensor.Event {
	var out []sensor.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func assertEventInFile(t *testing.T, path string, match func(sensor.Event) bool) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e sensor.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if match(e) {
			return
		}
	}
	t.Fatal("matching event not found in events file")
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH")
	}
}

func forceCleanupContainer(t *testing.T, name string) {
	t.Helper()
	exec.Command("docker", "rm", "-f", name).Run()
}

func getContainerPID(ctx context.Context, name string) (int, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect",
		"--format", "{{.State.Pid}}", name).Output()
	if err != nil {
		return 0, err
	}
	var pid int
	_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid)
	return pid, err
}
