// Package matcher implements the Phase 3 Prediction Matcher and IoC Rule Engine.
//
// Attribution model:
//  1. Hook layer intercepts agent tool calls → IoC Extractor → Prediction
//  2. Prediction Matcher joins Predictions against full tracee event stream
//  3. IoC Engine independently scans all events for known attack signatures
//  4. Match Report aggregates per-step results → RL reward signal
package matcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Prediction represents what the hook layer extracted from one agent tool call.
// It declares the syscall events expected to occur on a specific node within
// a time window after the tool call fires.
type Prediction struct {
	RunID         string    `json:"run_id"`
	AgentStep     int       `json:"agent_step"`
	Node          string    `json:"node"`           // target node name (matches sensor.Event.NodeID)
	TimeWindow    int       `json:"time_window"`    // seconds; events must fall in [SubmittedAt, SubmittedAt+window]
	SubmittedAt   time.Time `json:"submitted_at"`

	ExpectedEvents []ExpectedEvent `json:"expected_events"`
	TTP            string          `json:"ttp,omitempty"`           // MITRE ATT&CK ID
	ExtractorRule  string          `json:"extractor_rule,omitempty"` // which extraction rule produced this
	ToolCall       string          `json:"tool_call,omitempty"`      // original tool call (for debugging)
}

// ExpectedEvent is one expected syscall pattern inside a Prediction.
// Matching is partial: only the fields present in Args must match
// (the event may have additional fields).
type ExpectedEvent struct {
	Name string         `json:"name"`           // tracee event name, e.g. "execve"
	Args map[string]any `json:"args,omitempty"` // partial arg match; empty = match any args
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
