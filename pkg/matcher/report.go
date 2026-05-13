package matcher

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

// EpisodeReport is the output of one Matcher.Run() call.
// It contains all events attributed to the agent (anchor PID descendants).
type EpisodeReport struct {
	RunID        string         `json:"run_id"`
	GeneratedAt  time.Time      `json:"generated_at"`
	AnchorPID    int            `json:"anchor_pid"`
	NodeID       string         `json:"node_id"`
	TotalScanned int            `json:"total_events_scanned"`
	AttackEvents []sensor.Event `json:"attack_events"`
	EventsByType map[string]int `json:"events_by_type"`
}

// Save writes the EpisodeReport as JSON to path.
func (r *EpisodeReport) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// PrintSummary prints a human-readable summary to stdout.
func (r *EpisodeReport) PrintSummary() {
	fmt.Printf("Episode Report — run_id: %s\n", r.RunID)
	fmt.Printf("──────────────────────────────────────────\n")
	fmt.Printf("  anchor_pid:      %d\n", r.AnchorPID)
	fmt.Printf("  node:            %s\n", r.NodeID)
	fmt.Printf("  events scanned:  %d\n", r.TotalScanned)
	fmt.Printf("  attack events:   %d\n", len(r.AttackEvents))
	fmt.Println()
	fmt.Printf("  by type:\n")

	// Print sorted for deterministic output.
	types := make([]string, 0, len(r.EventsByType))
	for t := range r.EventsByType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Printf("    %-30s %d\n", t, r.EventsByType[t])
	}
}
