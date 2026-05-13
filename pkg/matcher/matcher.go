// Package matcher attributes eBPF sensor events to agent actions using
// PID tree ancestry. An "anchor PID" (the opencode/agent process running
// inside the attacker node) seeds a BFS over the (pid, ppid) graph embedded
// in the event stream. All events whose PID is a descendant of the anchor
// belong to the agent's episode.
//
// Previous time-window + IoC + reward approach has been removed.
package matcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

// Matcher performs PID-tree-based event attribution.
type Matcher struct{}

// NewMatcher returns a ready Matcher.
func NewMatcher() *Matcher { return &Matcher{} }

// RunFile reads events from eventsPath (file or directory) and returns an
// EpisodeReport filtered by anchorPID descendants.
//
// When eventsPath is a directory, all *.jsonl files are merged.
// When nodeID is non-empty, only events from that node are considered.
func (m *Matcher) RunFile(anchorPID int, eventsPath, nodeID, runID string) (*EpisodeReport, error) {
	events, err := loadEvents(eventsPath, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	// When loading a specific node file, pass nodeID="" to Run so the
	// node filter is not applied twice (the file already scopes to one node).
	filterNode := nodeID
	if isDir(eventsPath) && nodeID != "" {
		filterNode = nodeID // dir load already merged; still filter
	}
	return m.Run(anchorPID, events, filterNode, runID), nil
}

// Run executes attribution over an in-memory event slice.
func (m *Matcher) Run(anchorPID int, events []sensor.Event, nodeID, runID string) *EpisodeReport {
	descendants := BuildDescendants(events, anchorPID)
	attackEvents := FilterByPIDs(events, descendants, nodeID)

	byType := make(map[string]int)
	for _, ev := range attackEvents {
		byType[ev.Category]++
	}

	return &EpisodeReport{
		RunID:        runID,
		GeneratedAt:  time.Now(),
		AnchorPID:    anchorPID,
		NodeID:       nodeID,
		TotalScanned: len(events),
		AttackEvents: attackEvents,
		EventsByType: byType,
	}
}

// loadEvents loads events from path, which may be:
//   - a directory: all *.jsonl files are merged (optionally filtered to nodeID file)
//   - a single file: read directly
//
// When nodeID is non-empty and path is a directory, only <nodeID>.jsonl is read.
func loadEvents(path, nodeID string) ([]sensor.Event, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !fi.IsDir() {
		return readEventsFile(path)
	}

	// Directory: read one node file or merge all.
	if nodeID != "" {
		return readEventsFile(filepath.Join(path, nodeID+".jsonl"))
	}
	return readEventsDir(path)
}

// readEventsDir merges all *.jsonl files under dir.
func readEventsDir(dir string) ([]sensor.Event, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var all []sensor.Event
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		evs, err := readEventsFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		all = append(all, evs...)
	}
	return all, nil
}

func readEventsFile(path string) ([]sensor.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []sensor.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sensor.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
