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

// TraceeBackend implements Sensor by forking the tracee binary and parsing its
// JSON event stream from stdout.
//
// When TraceeBin is set to a path that exits immediately and prints pre-canned
// JSON lines, the backend becomes fully testable without eBPF.
type TraceeBackend struct {
	TraceeBin string // path to tracee binary (default: "tracee")
	Labeler   Labeler

	mu    sync.Mutex
	cmd   *exec.Cmd
	tree  *ProcessTreeBuilder
	out   chan Event
}

// Labeler is the minimal interface TraceeBackend needs to annotate events.
type Labeler interface {
	Annotate(e *Event)
}

// NewTraceeBackend creates a backend. labeler may be nil (no session annotation).
func NewTraceeBackend(bin string, labeler Labeler) *TraceeBackend {
	if bin == "" {
		bin = "tracee"
	}
	return &TraceeBackend{TraceeBin: bin, Labeler: labeler}
}

// Start forks tracee filtered to the given Docker container ID and returns the
// event channel. The channel is closed when the tracee process exits or ctx is
// cancelled.
func (t *TraceeBackend) Start(ctx context.Context, nodeID, containerID string) (<-chan Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.tree = NewProcessTreeBuilder(0)
	t.out = make(chan Event, 512)

	args := []string{
		"--output", "json",
		"--scope", fmt.Sprintf("container.id=%s", containerID),
		"--events", "execve,openat,connect,clone,fork,sched_process_exit",
	}

	cmd := exec.CommandContext(ctx, t.TraceeBin, args...)
	stdout, err := cmd.StdoutPipe()
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
