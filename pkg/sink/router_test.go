package sink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
)

func TestRoutingSink_RoutesToNodeFiles(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRoutingSink(dir)
	require.NoError(t, err)

	events := []sensor.Event{
		{NodeID: "node_attack", Category: "exec", PID: 100},
		{NodeID: "node_web", Category: "net", PID: 200},
		{NodeID: "node_attack", Category: "file", PID: 101},
		{NodeID: "node_db", Category: "exec", PID: 300},
	}
	for _, ev := range events {
		require.NoError(t, rs.Write(ev))
	}
	require.NoError(t, rs.Close())

	// node_attack.jsonl should have 2 events
	assertFileEvents(t, filepath.Join(dir, "node_attack.jsonl"), 2, "node_attack")
	// node_web.jsonl should have 1 event
	assertFileEvents(t, filepath.Join(dir, "node_web.jsonl"), 1, "node_web")
	// node_db.jsonl should have 1 event
	assertFileEvents(t, filepath.Join(dir, "node_db.jsonl"), 1, "node_db")
}

func TestRoutingSink_EmptyNodeIDFallback(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRoutingSink(dir)
	require.NoError(t, err)

	require.NoError(t, rs.Write(sensor.Event{NodeID: "", Category: "exec", PID: 1}))
	require.NoError(t, rs.Close())

	assertFileEvents(t, filepath.Join(dir, "_unknown.jsonl"), 1, "")
}

func TestRoutingSink_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRoutingSink(dir)
	require.NoError(t, err)

	const workers = 8
	const perWorker = 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodeID := []string{"node_a", "node_b"}[i%2]
			for j := 0; j < perWorker; j++ {
				rs.Write(sensor.Event{NodeID: nodeID, PID: i*1000 + j}) //nolint:errcheck
			}
		}()
	}
	wg.Wait()
	require.NoError(t, rs.Close())

	// Each node should have workers/2 * perWorker events.
	countA := countFileLines(t, filepath.Join(dir, "node_a.jsonl"))
	countB := countFileLines(t, filepath.Join(dir, "node_b.jsonl"))
	require.Equal(t, workers/2*perWorker, countA)
	require.Equal(t, workers/2*perWorker, countB)
}

func TestTruncateAll(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRoutingSink(dir)
	require.NoError(t, err)
	rs.Write(sensor.Event{NodeID: "node_attack", PID: 1}) //nolint:errcheck
	rs.Write(sensor.Event{NodeID: "node_web", PID: 2})    //nolint:errcheck
	rs.Close()                                             //nolint:errcheck

	// Files have content before truncation.
	require.Greater(t, fileSize(t, filepath.Join(dir, "node_attack.jsonl")), int64(0))

	require.NoError(t, TruncateAll(dir))

	require.Equal(t, int64(0), fileSize(t, filepath.Join(dir, "node_attack.jsonl")))
	require.Equal(t, int64(0), fileSize(t, filepath.Join(dir, "node_web.jsonl")))
}

func TestTruncateAll_NonExistentDir(t *testing.T) {
	require.NoError(t, TruncateAll("/tmp/sysbox-test-nonexistent-dir-xyz"))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func assertFileEvents(t *testing.T, path string, wantCount int, wantNodeID string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "missing file %s", path)

	lines := splitLines(data)
	require.Len(t, lines, wantCount)

	for _, line := range lines {
		var ev sensor.Event
		require.NoError(t, json.Unmarshal(line, &ev))
		if wantNodeID != "" {
			require.Equal(t, wantNodeID, ev.NodeID)
		}
	}
}

func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return len(splitLines(data))
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Size()
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
