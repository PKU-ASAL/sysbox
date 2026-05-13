// Package hook intercepts agent tool calls and extracts IoC-based Predictions
// before the tool is executed. The agent is unaware of this interception.
//
// Integration pattern:
//
//	h := hook.New(extractor, predictionsPath)
//	result, err := h.Wrap(call, func() error {
//	    return actualToolFn(call.Command)
//	})
package hook

import (
	"github.com/oslab/sysbox/pkg/matcher"
)

// ToolCall describes one agent tool invocation.
type ToolCall struct {
	ToolName  string `json:"tool_name"`  // "bash_exec", "ssh_exec", "http_request"
	Command   string `json:"command"`    // full command or URL
	Context   string `json:"context"`    // agent chain-of-thought (optional)
	Node      string `json:"node"`       // target node (parsed from params or set by framework)
	RunID     string `json:"run_id"`
	AgentStep int    `json:"agent_step"`
}

// Hook wraps tool executors, extracts predictions, and records them.
type Hook struct {
	extractor       Extractor
	predictionsPath string
}

// New creates a Hook.
func New(extractor Extractor, predictionsPath string) *Hook {
	return &Hook{extractor: extractor, predictionsPath: predictionsPath}
}

// Wrap extracts a Prediction (with StartTS = now) from the tool call, records
// it, then calls executeFn. Errors from prediction writing do not abort
// the tool execution.
func (h *Hook) Wrap(call ToolCall, executeFn func() error) (matcher.Prediction, error) {
	pred := h.extractor.Extract(call) // sets pred.StartTS = now

	if h.predictionsPath != "" && len(pred.ExpectedEvents) > 0 {
		_ = matcher.AppendPrediction(h.predictionsPath, pred)
	}

	return pred, executeFn()
}
