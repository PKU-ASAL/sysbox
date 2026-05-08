package sensor

import "sync"

// ProcInfo is one entry in the process table.
type ProcInfo struct {
	Comm string
	PPID int
}

// ProcessTreeBuilder maintains a live pid→ancestry map by consuming raw
// Tracee events. It is an internal helper; process ancestry is NOT exported
// on Event objects.
//
// Thread-safe: Feed and Ancestry may be called from different goroutines.
type ProcessTreeBuilder struct {
	mu    sync.RWMutex
	procs map[int]ProcInfo
}

// NewProcessTreeBuilder returns an empty builder seeded with the container
// init process (pid=1 is the sleep-infinity, labelled "node-init").
func NewProcessTreeBuilder(initHostPID int) *ProcessTreeBuilder {
	b := &ProcessTreeBuilder{procs: make(map[int]ProcInfo)}
	if initHostPID > 0 {
		b.procs[initHostPID] = ProcInfo{Comm: "node-init", PPID: 0}
	}
	return b
}

// Feed consumes one raw Tracee event (parsed from JSON) and updates the
// internal process table.
//
// Recognised event names:
//   - "clone" / "fork" / "vfork": register child PID inheriting parent comm
//   - "execve": update comm for the executing PID
//   - "sched_process_exit": remove the exiting PID
//   - all others: no-op
func (b *ProcessTreeBuilder) Feed(raw map[string]any) {
	name, _ := raw["eventName"].(string)
	pid := intField(raw, "hostProcessId")
	ppid := intField(raw, "hostParentProcessId")
	comm, _ := raw["processName"].(string)

	b.mu.Lock()
	defer b.mu.Unlock()

	switch name {
	case "clone", "fork", "vfork":
		// Child PID is reported as the return value or a dedicated arg.
		childPID := intField(raw, "returnValue")
		if childPID <= 0 {
			childPID = argInt(raw, "child_tid")
		}
		if childPID > 0 && comm != "" {
			b.procs[childPID] = ProcInfo{Comm: comm, PPID: pid}
		}
		// Ensure parent is recorded.
		if pid > 0 {
			if _, ok := b.procs[pid]; !ok {
				b.procs[pid] = ProcInfo{Comm: comm, PPID: ppid}
			}
		}
	case "execve":
		// Update comm for the process that just exec'd.
		if pid > 0 {
			b.procs[pid] = ProcInfo{Comm: comm, PPID: ppid}
		}
	case "sched_process_exit":
		delete(b.procs, pid)
	default:
		// Ensure at least the PID is recorded even for non-lifecycle events.
		if pid > 0 {
			if _, ok := b.procs[pid]; !ok && comm != "" {
				b.procs[pid] = ProcInfo{Comm: comm, PPID: ppid}
			}
		}
	}
}

// Ancestry returns the chain of comm names from the container init (pid=1
// in container, labelled "node-init") down to pid.
// Example: ["node-init", "sshd", "bash", "nmap"]
//
// Unknown ancestors are represented as "?". The chain is capped at depth 32
// to prevent infinite loops from stale/corrupt tables.
func (b *ProcessTreeBuilder) Ancestry(pid int) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var chain []string
	visited := map[int]bool{}
	cur := pid
	for i := 0; i < 32; i++ {
		if visited[cur] {
			break
		}
		visited[cur] = true

		info, ok := b.procs[cur]
		if !ok {
			chain = append(chain, "?")
			break
		}
		chain = append(chain, info.Comm)
		if info.PPID == 0 || info.PPID == cur {
			break
		}
		cur = info.PPID
	}

	// Reverse so chain reads root→leaf.
	for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
		chain[l], chain[r] = chain[r], chain[l]
	}
	return chain
}

// Snapshot returns a copy of the current process table (for testing/debugging).
func (b *ProcessTreeBuilder) Snapshot() map[int]ProcInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[int]ProcInfo, len(b.procs))
	for k, v := range b.procs {
		out[k] = v
	}
	return out
}

func intField(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func argInt(raw map[string]any, argName string) int {
	args, _ := raw["args"].([]any)
	for _, a := range args {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if m["name"] == argName {
			return intField(m, "value")
		}
	}
	return 0
}
