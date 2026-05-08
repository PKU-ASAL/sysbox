// Package hook intercepts agent tool calls and extracts IoC-based Predictions
// before the tool is executed. The agent is unaware of this interception.
//
// Integration pattern:
//
//	hook := hook.New(extractor, predictionsPath, runID)
//	result, err := hook.Wrap(call, func() (string, error) {
//	    return actualToolFn(call.Command)
//	})
package hook

import (
	"time"

	"github.com/oslab/sysbox/pkg/matcher"
)

// ToolCall describes one agent tool invocation.
type ToolCall struct {
	ToolName  string `json:"tool_name"`  // "bash_exec", "ssh_exec", "http_request"
	Command   string `json:"command"`    // full command or URL
	Context   string `json:"context"`    // agent's chain-of-thought (optional)
	Node      string `json:"node"`       // target node (parsed from tool params or set by framework)
	RunID     string `json:"run_id"`
	AgentStep int    `json:"agent_step"`
}

// Hook wraps tool executors, extracts predictions, and records them.
type Hook struct {
	extractor      Extractor
	predictionsPath string
	timeWindow     int // seconds for prediction window (default 30)
}

// New creates a Hook.
// predictionsPath: where to append predictions (JSONL).
func New(extractor Extractor, predictionsPath string) *Hook {
	return &Hook{
		extractor:       extractor,
		predictionsPath: predictionsPath,
		timeWindow:      30,
	}
}

// WithTimeWindow sets the time window for predictions (default 30s).
func (h *Hook) WithTimeWindow(seconds int) *Hook {
	h.timeWindow = seconds
	return h
}

// Wrap extracts a prediction from the tool call, records it, then runs executeFn.
// The returned prediction is provided for testing/logging; errors from
// prediction writing do not abort the tool execution.
func (h *Hook) Wrap(call ToolCall, executeFn func() error) (matcher.Prediction, error) {
	pred := h.extractor.Extract(call)
	pred.SubmittedAt = time.Now()
	if pred.TimeWindow == 0 {
		pred.TimeWindow = h.timeWindow
	}

	// Write prediction (best-effort; don't fail tool call if write fails).
	if h.predictionsPath != "" && len(pred.ExpectedEvents) > 0 {
		_ = matcher.AppendPrediction(h.predictionsPath, pred)
	}

	return pred, executeFn()
}
