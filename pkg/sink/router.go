package sink

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/oslab/sysbox/pkg/sensor"
)

// RoutingSink routes events to per-node JSONL files under a directory.
//
// Each unique Event.NodeID gets its own file: <dir>/<node_id>.jsonl
// Events with an empty NodeID go to <dir>/_unknown.jsonl.
//
// Files are created lazily on first write; the directory is created up front.
// Concurrent writes for different nodes are safe: the outer mutex protects
// the node→sink map, while each JSONLSink carries its own internal lock.
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
	// Ensure the directory and any pre-existing event files are accessible
	// to non-root users (e.g. episode runner truncating between episodes).
	// This runs at sensor start, so it covers files left by a previous run
	// before the first Write call has a chance to chmod newly opened files.
	_ = os.Chmod(dir, 0o777)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				_ = os.Chmod(filepath.Join(dir, e.Name()), 0o666)
			}
		}
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

// TruncateAll truncates every *.jsonl file in the sink directory to zero bytes.
// Used by the episode runner at episode start to ensure a clean slate while
// preserving open file descriptors held by the sensor process.
func TruncateAll(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		if f, err := os.OpenFile(filepath.Join(dir, e.Name()),
			os.O_WRONLY|os.O_TRUNC, 0); err == nil {
			f.Close()
		}
	}
	return nil
}
