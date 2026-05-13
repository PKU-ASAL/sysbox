package matcher

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
)

func ev(pid, ppid int, _ string, nodeID string) sensor.Event {
	return sensor.Event{PID: pid, PPID: ppid, NodeID: nodeID}
}

// TestBuildDescendants verifies that BFS correctly finds all descendants.
func TestBuildDescendants(t *testing.T) {
	events := []sensor.Event{
		ev(100, 1, "fork", "node_attack"),   // anchor = 100
		ev(200, 100, "fork", "node_attack"), // child of anchor
		ev(300, 200, "execve", "node_attack"), // grandchild
		ev(400, 300, "openat", "node_attack"), // great-grandchild
		ev(999, 50, "execve", "node_attack"),  // unrelated process
	}

	desc := BuildDescendants(events, 100)

	require.True(t, desc[100], "anchor itself must be included")
	require.True(t, desc[200], "direct child must be included")
	require.True(t, desc[300], "grandchild must be included")
	require.True(t, desc[400], "great-grandchild must be included")
	require.False(t, desc[999], "unrelated pid must be excluded")
}

// TestBuildDescendantsIsolation checks that two separate process trees
// don't bleed into each other.
func TestBuildDescendantsIsolation(t *testing.T) {
	events := []sensor.Event{
		ev(10, 1, "fork", "node_attack"),
		ev(11, 10, "execve", "node_attack"),
		ev(20, 1, "fork", "node_web"),  // different root
		ev(21, 20, "execve", "node_web"),
	}

	desc := BuildDescendants(events, 10)
	require.True(t, desc[10])
	require.True(t, desc[11])
	require.False(t, desc[20])
	require.False(t, desc[21])
}

// TestFilterByPIDs verifies node_id filtering works alongside PID filtering.
func TestFilterByPIDs(t *testing.T) {
	events := []sensor.Event{
		ev(100, 1, "execve", "node_attack"),
		ev(200, 100, "openat", "node_attack"),
		ev(100, 1, "execve", "node_web"), // same PID, different node
	}
	pids := map[int]bool{100: true, 200: true}

	all := FilterByPIDs(events, pids, "")
	require.Len(t, all, 3)

	attackOnly := FilterByPIDs(events, pids, "node_attack")
	require.Len(t, attackOnly, 2)
	for _, e := range attackOnly {
		require.Equal(t, "node_attack", e.NodeID)
	}
}

// TestMatcherRun exercises the full Matcher.Run path.
func TestMatcherRun(t *testing.T) {
	events := []sensor.Event{
		ev(1000, 1, "execve", "node_attack"),   // opencode (anchor)
		ev(1001, 1000, "fork", "node_attack"),
		ev(1002, 1001, "execve", "node_attack"), // bash
		ev(1003, 1002, "openat", "node_attack"), // file access
		ev(5000, 999, "execve", "node_web"),     // victim — must be excluded
	}

	m := NewMatcher()
	report := m.Run(1000, events, "node_attack", "test-run")

	require.Equal(t, 1000, report.AnchorPID)
	require.Equal(t, "node_attack", report.NodeID)
	require.Equal(t, len(events), report.TotalScanned)

	// Only events from node_attack and descendants of 1000 should appear.
	require.Len(t, report.AttackEvents, 4)
	for _, e := range report.AttackEvents {
		require.Equal(t, "node_attack", e.NodeID)
	}
}
