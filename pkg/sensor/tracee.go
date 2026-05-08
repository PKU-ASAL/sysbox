package sensor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// TraceeBackend implements Sensor by forking the tracee binary (or running the
// aquasec/tracee Docker image in privileged mode) and parsing its JSON event
// stream from stdout.
//
// Field name reference for tracee v0.22+:
//
//	cgroupId, hostProcessId, hostParentProcessId, processName, eventName,
//	args[{name, type, value}]
type TraceeBackend struct {
	// TraceeBin is the path to the tracee binary.
	// Set to "" to use DockerImage mode instead.
	TraceeBin string
	// DockerImage is used when TraceeBin == "" or DockerMode is true.
	// Defaults to "aquasec/tracee:0.22.0".
	DockerImage string
	// DockerMode forces running tracee via `docker run --privileged` even
	// when TraceeBin is set. Useful for non-root users who are in the
	// docker group.
	DockerMode bool
	// Events is the comma-separated list of events to capture.
	// Defaults to sensible Phase 2 defaults.
	Events string

	Labeler Labeler

	mu  sync.Mutex
	cmd *exec.Cmd
	tree *ProcessTreeBuilder
	out  chan Event
}

// Labeler is the minimal interface TraceeBackend needs to annotate events.
type Labeler interface {
	Annotate(e *Event)
}

// NewTraceeBackend creates a backend. labeler may be nil (no session annotation).
func NewTraceeBackend(bin string, labeler Labeler) *TraceeBackend {
	return &TraceeBackend{
		TraceeBin:   bin,
		DockerImage: "aquasec/tracee:0.22.0",
		Labeler:     labeler,
	}
}

// NewDockerTraceeBackend creates a backend that runs tracee via Docker.
// Use this when running as a non-root user who is in the docker group.
func NewDockerTraceeBackend(image string, labeler Labeler) *TraceeBackend {
	if image == "" {
		image = "aquasec/tracee:0.22.0"
	}
	return &TraceeBackend{
		DockerImage: image,
		DockerMode:  true,
		Labeler:     labeler,
	}
}

// defaultEvents returns the standard Phase 2 event set.
func defaultEvents() string {
	return "execve,execveat,openat,connect,clone,fork,vfork,sched_process_exit"
}

// Start forks tracee and returns the event channel.
//
// containerID controls the scope:
//   - non-empty: filter to that specific Docker container (best for single-container tests)
//   - empty:     capture all container events (use when session cgroups may be outside
//                Docker's cgroup hierarchy, i.e. under /sys/fs/cgroup/sysbox.slice/)
//
// When session cgroups are created outside Docker's cgroup subtree (which is the
// production case with sysbox.slice), pass containerID="" so tracee sees events
// from those processes after they move to the session cgroup.
// The Labeler then handles session attribution via cgroup_id mapping.
func (t *TraceeBackend) Start(ctx context.Context, nodeID, containerID string) (<-chan Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.tree = NewProcessTreeBuilder(0)
	t.out = make(chan Event, 512)

	events := t.Events
	if events == "" {
		events = defaultEvents()
	}

	var cmd *exec.Cmd
	if t.DockerMode || t.TraceeBin == "" {
		img := t.DockerImage
		if img == "" {
			img = "aquasec/tracee:0.22.0"
		}

		// Build scope argument.
		// If containerID is set, scope to that container.
		// Otherwise use --scope=container to see all container events.
		// Note: processes that move OUT of the Docker cgroup (into sysbox.slice)
		// are no longer "in a container" per tracee, so we can't use container
		// scope for session cgroup tracking. Use global scope instead.
		baseArgs := []string{
			"run", "--rm", "--privileged",
			"--pid=host",
			"--cgroupns=host",
			"-v", "/etc/os-release:/etc/os-release-host:ro",
			"-v", "/sys/kernel/btf/vmlinux:/sys/kernel/btf/vmlinux:ro",
			"-v", "/sys/fs/bpf:/sys/fs/bpf",
			"-v", "/sys/fs/cgroup:/sys/fs/cgroup",
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			img,
		}

		// Build tracee args.
		// If containerID is given, scope to that container (single-container mode).
		// Otherwise omit --scope entirely so tracee captures ALL events.
		// All-events mode is required when session cgroups live outside Docker's
		// cgroup hierarchy (/sys/fs/cgroup/sysbox.slice/); the Labeler then does
		// the session attribution via cgroup_id mapping.
		traceeArgs := []string{
			"--cri=docker:/var/run/docker.sock",
			fmt.Sprintf("--events=%s", events),
			"--output=json",
		}
		if containerID != "" {
			scopeID := containerID
			if len(scopeID) > 12 {
				scopeID = scopeID[:12]
			}
			traceeArgs = append([]string{fmt.Sprintf("--scope=container=%s", scopeID)}, traceeArgs...)
		}

		dockerArgs := append(baseArgs, traceeArgs...)
		cmd = exec.CommandContext(ctx, "docker", dockerArgs...)
	} else {
		// Run the tracee binary directly (requires root/CAP_BPF).
		args := []string{
			"--cri=docker:/var/run/docker.sock",
			fmt.Sprintf("--events=%s", events),
			"--output=json",
		}
		cmd = exec.CommandContext(ctx, t.TraceeBin, args...)
	}

	stdout, err := cmd.StdoutPipe()
	// Discard stderr to avoid noise from tracee's own logging.
	cmd.Stderr = nil
	if err != nil {
		return nil, fmt.Errorf("tracee stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tracee start: %w", err)
	}
	t.cmd = cmd

	go t.readLoop(nodeID, stdout)
	return t.out, nil
}

func (t *TraceeBackend) readLoop(nodeID string, r io.Reader) {
	defer close(t.out)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB per line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		t.tree.Feed(raw)

		e := parseEvent(nodeID, raw)
		if t.Labeler != nil {
			t.Labeler.Annotate(&e)
		}
		t.out <- e
	}
}

// Stop terminates the tracee subprocess.
func (t *TraceeBackend) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd != nil && t.cmd.Process != nil {
		return t.cmd.Process.Kill()
	}
	return nil
}

// ProcessTree returns the internal ProcessTreeBuilder for Phase 3 Matcher queries.
func (t *TraceeBackend) ProcessTree() *ProcessTreeBuilder { return t.tree }

// parseEvent converts a raw Tracee JSON map into an Event.
//
// Tracee v0.20+ field names:
//
//	timestamp, hostProcessId, hostParentProcessId, cgroupId,
//	eventName, processName, args[{name,type,value}]
func parseEvent(nodeID string, raw map[string]any) Event {
	e := Event{
		NodeID:    nodeID,
		Timestamp: int64(floatField(raw, "timestamp")),
		PID:       intField(raw, "hostProcessId"),
		PPID:      intField(raw, "hostParentProcessId"),
		CgroupID:  uint64(floatField(raw, "cgroupId")),
		Name:      strField(raw, "eventName"),
	}

	// Classify event type.
	switch e.Name {
	case "execve", "execveat":
		e.Type = "syscall"
	case "openat", "open":
		e.Type = "file"
	case "connect", "accept", "accept4", "bind":
		e.Type = "net"
	default:
		e.Type = "syscall"
	}

	// Parse args array into a flat map.
	if args, ok := raw["args"].([]any); ok {
		m := make(map[string]any, len(args))
		for _, a := range args {
			if arg, ok := a.(map[string]any); ok {
				if name, ok := arg["name"].(string); ok {
					m[name] = arg["value"]
				}
			}
		}
		e.Args = m
	}

	return e
}

func floatField(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// MockBackend is a test double that replays pre-canned JSON event lines.
// Use it in unit tests where the tracee binary is unavailable.
type MockBackend struct {
	Lines  []string // raw JSON lines to replay
	nodeID string
	tree   *ProcessTreeBuilder
	lab    Labeler
}

func NewMockBackend(lines []string, labeler Labeler) *MockBackend {
	return &MockBackend{Lines: lines, lab: labeler}
}

func (m *MockBackend) Start(_ context.Context, nodeID, _ string) (<-chan Event, error) {
	m.nodeID = nodeID
	m.tree = NewProcessTreeBuilder(0)
	ch := make(chan Event, len(m.Lines)+1)
	for _, line := range m.Lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		m.tree.Feed(raw)
		e := parseEvent(nodeID, raw)
		if m.lab != nil {
			m.lab.Annotate(&e)
		}
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *MockBackend) Stop() error                  { return nil }
func (m *MockBackend) ProcessTree() *ProcessTreeBuilder { return m.tree }
