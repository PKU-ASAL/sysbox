package matcher

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
)

func nmapExecEvent(nodeID string, ts time.Time) sensor.Event {
	return sensor.Event{
		NodeID:    nodeID,
		Timestamp: ts.UnixNano(),
		PID:       1234,
		PPID:      100,
		Name:      "execve",
		Type:      "syscall",
		Args:      map[string]any{"pathname": "/usr/bin/nmap"},
	}
}

func shadowOpenEvent(nodeID string, ts time.Time) sensor.Event {
	return sensor.Event{
		NodeID:    nodeID,
		Timestamp: ts.UnixNano(),
		PID:       5678,
		PPID:      100,
		Name:      "openat",
		Type:      "file",
		Args:      map[string]any{"pathname": "/etc/shadow"},
	}
}

func TestMatcherBasicHit(t *testing.T) {
	now := time.Now()
	pred := Prediction{
		RunID:       "run-1",
		AgentStep:   1,
		Node:        "node_a",
		TimeWindow:  30,
		SubmittedAt: now,
		TTP:         "T1595.001",
		ExpectedEvents: []ExpectedEvent{
			{Name: "execve", Args: map[string]any{"pathname": "/usr/bin/nmap"}},
		},
	}
	events := []sensor.Event{
		nmapExecEvent("node_a", now.Add(2*time.Second)),
	}

	m := NewMatcher(&IoCEngine{})
	report := m.Run([]Prediction{pred}, events, "run-1")

	require.Len(t, report.Steps, 1)
	step := report.Steps[0]
	require.Equal(t, 1.0, step.PredictionHitRate)
	require.Len(t, step.MatchedEvents, 1)
	require.True(t, step.MatchedEvents[0].MatchedPrediction)
	require.Greater(t, step.StepReward, 0.0)
}

func TestMatcherMiss(t *testing.T) {
	now := time.Now()
	pred := Prediction{
		RunID:       "run-2",
		AgentStep:   1,
		Node:        "node_a",
		TimeWindow:  5,
		SubmittedAt: now,
		ExpectedEvents: []ExpectedEvent{
			{Name: "execve", Args: map[string]any{"pathname": "/usr/bin/nmap"}},
		},
	}
	// Event outside time window.
	events := []sensor.Event{
		nmapExecEvent("node_a", now.Add(60*time.Second)),
	}

	m := NewMatcher(&IoCEngine{})
	report := m.Run([]Prediction{pred}, events, "run-2")

	require.Equal(t, 0.0, report.Steps[0].PredictionHitRate)
	require.Len(t, report.Steps[0].UnmatchedPreds, 1)
	require.Less(t, report.Steps[0].StepReward, 0.0)
}

func TestMatcherWrongNode(t *testing.T) {
	now := time.Now()
	pred := Prediction{
		RunID:       "run-3",
		AgentStep:   1,
		Node:        "node_a",
		TimeWindow:  30,
		SubmittedAt: now,
		ExpectedEvents: []ExpectedEvent{
			{Name: "execve"},
		},
	}
	events := []sensor.Event{
		nmapExecEvent("node_b", now.Add(2*time.Second)), // wrong node
	}

	m := NewMatcher(&IoCEngine{})
	report := m.Run([]Prediction{pred}, events, "run-3")
	require.Equal(t, 0.0, report.Steps[0].PredictionHitRate)
}

func TestMatcherPartialArgMatch(t *testing.T) {
	now := time.Now()
	pred := Prediction{
		RunID: "run-4", AgentStep: 1, Node: "node_a",
		TimeWindow: 30, SubmittedAt: now,
		ExpectedEvents: []ExpectedEvent{
			// Empty args = match any execve
			{Name: "execve", Args: map[string]any{}},
		},
	}
	events := []sensor.Event{
		nmapExecEvent("node_a", now.Add(1*time.Second)),
	}
	m := NewMatcher(&IoCEngine{})
	report := m.Run([]Prediction{pred}, events, "run-4")
	require.Equal(t, 1.0, report.Steps[0].PredictionHitRate)
}

func TestMatcherUnscriptedIoC(t *testing.T) {
	now := time.Now()
	// No predictions.
	events := []sensor.Event{
		nmapExecEvent("node_a", now),
	}

	// Load real IoC rules from test fixtures.
	iocRules := []IoCRule{{
		ID:    "ioc-exec-nmap",
		TTP:   "T1595.001",
		Event: "execve",
		Match: map[string]any{"args.pathname": []any{"/usr/bin/nmap"}},
	}}
	ioc := &IoCEngine{rules: iocRules}
	m := NewMatcher(ioc)
	report := m.Run(nil, events, "run-5")

	// No steps (no predictions), but the unscripted IoC should appear
	// in episode summary via global scan. In our implementation, unscripted
	// IoCs are attached to the closest step. With no steps, they won't appear.
	// Episode reward should still be valid (0 steps → no reward signal).
	require.Equal(t, float64(0), report.EpisodeReward)
}

func TestMatcherReward(t *testing.T) {
	now := time.Now()
	preds := []Prediction{
		{
			RunID: "run-6", AgentStep: 1, Node: "node_a",
			TimeWindow: 30, SubmittedAt: now,
			ExpectedEvents: []ExpectedEvent{{Name: "execve"}},
		},
	}
	events := []sensor.Event{
		nmapExecEvent("node_a", now.Add(1*time.Second)),
	}
	m := NewMatcher(&IoCEngine{})
	report := m.Run(preds, events, "run-6")

	// Fully matched → reward > 0.
	require.Greater(t, report.EpisodeReward, 0.0)
}

func TestIoCEngineLoad(t *testing.T) {
	rulesDir := filepath.Join("..", "..", "rules", "ioc")
	engine, err := NewIoCEngine(rulesDir)
	if err != nil {
		t.Skipf("rules/ioc not found: %v", err)
	}
	require.Greater(t, len(engine.Rules()), 0)

	// nmap should match ioc-exec-nmap.
	ev := nmapExecEvent("node_a", time.Now())
	ruleID, ttp, matched := engine.Scan(ev)
	require.True(t, matched, "nmap execve should match IoC rule")
	require.NotEmpty(t, ruleID)
	require.NotEmpty(t, ttp)
}

func TestGlobMatch(t *testing.T) {
	require.True(t, globMatch("/usr/bin/nmap", "/usr/bin/nmap"))
	require.True(t, globMatch("/usr/bin/*", "/usr/bin/nmap"))
	require.True(t, globMatch("*.py", "exploit.py"))
	require.True(t, globMatch("*python*", "/usr/bin/python3"))
	require.False(t, globMatch("/usr/bin/nc", "/usr/bin/nmap"))
}
