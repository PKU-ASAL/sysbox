package hook

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleExtractorNmap(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules", "extraction")
	ext, err := NewRuleExtractor(rulesDir)
	require.NoError(t, err)
	require.Greater(t, len(ext.rules), 0)

	pred := ext.Extract(ToolCall{
		ToolName:  "bash_exec",
		Command:   "nmap -p 22,80 10.0.1.0/24",
		Node:      "node_a",
		RunID:     "run-1",
		AgentStep: 3,
	})

	require.Equal(t, "run-1", pred.RunID)
	require.Equal(t, 3, pred.AgentStep)
	require.Equal(t, "T1595.001", pred.TTP)
	require.Greater(t, len(pred.ExpectedEvents), 0)

	// Should predict execve(nmap).
	found := false
	for _, ev := range pred.ExpectedEvents {
		if ev.Name == "execve" {
			found = true
		}
	}
	require.True(t, found, "expected execve prediction for nmap command")
}

func TestRuleExtractorSSH(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules", "extraction")
	ext, err := NewRuleExtractor(rulesDir)
	require.NoError(t, err)

	pred := ext.Extract(ToolCall{
		ToolName:  "bash_exec",
		Command:   "ssh root@10.0.2.5 id",
		Node:      "node_a",
		RunID:     "run-2",
		AgentStep: 5,
	})

	require.Equal(t, "T1021.004", pred.TTP)
	require.NotEmpty(t, pred.ExpectedEvents)
}

func TestRuleExtractorNoMatch(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules", "extraction")
	ext, err := NewRuleExtractor(rulesDir)
	require.NoError(t, err)

	pred := ext.Extract(ToolCall{
		ToolName:  "bash_exec",
		Command:   "echo hello",
		Node:      "node_a",
		RunID:     "run-3",
		AgentStep: 1,
	})

	// No rules match "echo hello" → empty expected events.
	require.Empty(t, pred.ExpectedEvents)
}

func TestHookWrap(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules", "extraction")
	ext, err := NewRuleExtractor(rulesDir)
	require.NoError(t, err)

	executed := false
	h := New(ext, "") // no file write

	call := ToolCall{
		ToolName: "bash_exec", Command: "nmap 10.0.1.0/24",
		Node: "node_a", RunID: "run-x", AgentStep: 1,
	}
	pred, err := h.Wrap(call, func() error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	require.True(t, executed, "tool call should be executed")
	require.NotEmpty(t, pred.ExpectedEvents, "hook should extract prediction")
}
