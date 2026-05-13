// Package matcher implements the Phase 3 Prediction Matcher and IoC Rule Engine.
//
// Attribution model:
//  1. Observation layer records per-step bash timestamps (StartTS/EndTS in ms)
//     via the opencode SSE stream or the hook PreToolUse/PostToolUse callbacks.
//  2. Prediction Matcher joins Predictions against the full tracee event stream
//     using the exact bash execution window [StartTS, EndTS+buffer].
//  3. IoC Engine independently scans all in-window events for attack signatures.
//  4. Match Report aggregates per-step results → RL reward signal.
package matcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Prediction represents one agent bash step and the events expected within it.
//
// Time attribution uses StartTS and EndTS (unix milliseconds).
//   - StartTS: when the bash command started executing (required).
//   - EndTS:   when it finished. 0 means still running or unknown;
//     the matcher uses StartTS+DefaultEndWindowMs as a fallback.
//
// ExpectedEvents may be empty (no extraction rules matched); the matcher then
// scores hit_rate=1.0 and relies solely on unscripted IoC discovery.
type Prediction struct {
	RunID     string `json:"run_id"`
	AgentStep int    `json:"agent_step"`
	Node      string `json:"node"` // must match sensor.Event.NodeID

	// Timestamps from the opencode SSE stream or hook callbacks (unix milliseconds).
	StartTS int64 `json:"start_ts"`
	EndTS   int64 `json:"end_ts,omitempty"` // 0 → DefaultEndWindowMs fallback

	ExpectedEvents []ExpectedEvent `json:"expected_events,omitempty"`
	TTP            string          `json:"ttp,omitempty"`
	ExtractorRule  string          `json:"extractor_rule,omitempty"`
	Command        string          `json:"command,omitempty"` // raw bash command (for debugging)
}

// ExpectedEvent is one expected syscall pattern inside a Prediction.
// Matching is partial: only the fields present in Args must match.
type ExpectedEvent struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// ReadPredictions reads all Predictions from a JSONL file.
func ReadPredictions(path string) ([]Prediction, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Prediction
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var p Prediction
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("parse prediction: %w", err)
		}
		out = append(out, p)
	}
	return out, scanner.Err()
}

// AppendPrediction appends a single Prediction to a JSONL file.
func AppendPrediction(path string, p Prediction) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(p)
}
