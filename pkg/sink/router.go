package sink

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

// RoutingSink routes events to per-node JSONL files under a directory.
//
// Each unique Event.NodeID gets its own file: <dir>/<node_id>.jsonl
// Events with an empty NodeID go to <dir>/_unknown.jsonl.
//
// Files are created lazily on first write; the directory is created up front
// with default permissions (0o755 / 0o644). Out-of-process consumers should
// read these files, not mutate them — episode boundaries are the caller's
// concern, not the sink's.
type RoutingSink struct {
	dir  string
	mu   sync.Mutex
	open map[string]*JSONLSink
}

// NewRoutingSink creates a RoutingSink that writes per-node files to dir.
// The directory is created if it does not exist.
func NewRoutingSink(dir string) (*RoutingSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create events dir %s: %w", dir, err)
	}
	return &RoutingSink{dir: dir, open: make(map[string]*JSONLSink)}, nil
}

func (r *RoutingSink) Write(e sensor.Event) error {
	nodeID := e.NodeID
	if nodeID == "" {
		nodeID = "_unknown"
	}

	r.mu.Lock()
	s, ok := r.open[nodeID]
	if !ok {
		s = NewJSONLSink(filepath.Join(r.dir, nodeID+".jsonl"))
		r.open[nodeID] = s
	}
	r.mu.Unlock()

	return s.Write(e)
}

// Close flushes and closes all open per-node sinks.
func (r *RoutingSink) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var last error
	for _, s := range r.open {
		if err := s.Close(); err != nil {
			last = err
		}
	}
	r.open = make(map[string]*JSONLSink)
	return last
}

// WriteSessionMarker writes a synthetic meta event to every node file the
// caller knows about, so downstream analysis can self-describe the boundary
// of one sensor run without coordinating with the runner.
//
// The marker is a regular sensor.Event with Category="meta" and a small JSON
// payload in Raw. NodeID is the per-file routing key.
func (r *RoutingSink) WriteSessionMarker(nodes []string, runID string) error {
	now := time.Now().UnixNano()
	payload := fmt.Sprintf(`{"meta":"sensor_start","sensor_run_id":%q,"started_at":%d}`, runID, now)
	for _, n := range nodes {
		ev := sensor.Event{
			NodeID:    n,
			Timestamp: now,
			Category:  "meta",
			Raw:       []byte(payload),
		}
		if err := r.Write(ev); err != nil {
			return err
		}
	}
	return nil
}
