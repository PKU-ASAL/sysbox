package matcher

import "github.com/oslab/sysbox/pkg/sensor"

// BuildDescendants returns the set of all PIDs that are descendants of
// anchorPID (inclusive). It reconstructs the process tree from the (pid, ppid)
// pairs present in the event stream — no /proc access required.
//
// Algorithm: build a parent→children adjacency map from all events,
// then BFS from anchorPID.
func BuildDescendants(events []sensor.Event, anchorPID int) map[int]bool {
	// parent → set of children
	children := make(map[int][]int)
	seen := make(map[[2]int]bool) // dedup (ppid,pid) pairs
	for _, ev := range events {
		if ev.PID <= 0 || ev.PPID <= 0 {
			continue
		}
		key := [2]int{ev.PPID, ev.PID}
		if seen[key] {
			continue
		}
		seen[key] = true
		children[ev.PPID] = append(children[ev.PPID], ev.PID)
	}

	result := make(map[int]bool)
	queue := []int{anchorPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if result[pid] {
			continue
		}
		result[pid] = true
		queue = append(queue, children[pid]...)
	}
	return result
}

// FilterByPIDs returns the subset of events whose PID is in the given set.
// If nodeID is non-empty, additionally filters by event.NodeID — but only
// when events actually carry a non-empty NodeID (sidecar/tree-scoped events
// have NodeID="" because the sensor writes them without node attribution;
// in that case the tree scope already constrains the event set).
func FilterByPIDs(events []sensor.Event, pids map[int]bool, nodeID string) []sensor.Event {
	// Determine whether any event has a non-empty NodeID.
	hasNodeID := false
	for i := range events {
		if events[i].NodeID != "" {
			hasNodeID = true
			break
		}
	}
	applyNodeFilter := nodeID != "" && hasNodeID

	var out []sensor.Event
	for _, ev := range events {
		if applyNodeFilter && ev.NodeID != nodeID {
			continue
		}
		if pids[ev.PID] {
			out = append(out, ev)
		}
	}
	return out
}
