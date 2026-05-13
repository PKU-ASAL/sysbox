//go:build e2e

// Package e2e Phase 3 Matcher end-to-end tests.
//
// These tests validate the full Prediction Matcher + IoC Engine pipeline
// using synthetic events.jsonl and predictions.jsonl files.
// No Docker, tracee, or root privileges required.
//
// Run with:
//
//	go test ./tests/e2e/... -tags=e2e -v -run TestMatcher -timeout 60s
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/matcher"
	"github.com/oslab/sysbox/pkg/sensor"
)

// findRulesDir returns the absolute path to the rules/ directory in the repo.
func findRulesDir(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	dir := filepath.Join(root, "rules")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatalf("rules/ directory not found at %s", dir)
	}
	return dir
}

// writeEvents writes a slice of sensor.Events as JSONL to path.
func writeEvents(t *testing.T, path string, events []sensor.Event) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		require.NoError(t, enc.Encode(ev))
	}
}

// writePredictions writes a slice of Predictions as JSONL to path.
func writePredictions(t *testing.T, path string, preds []matcher.Prediction) {
	t.Helper()
	for _, p := range preds {
		require.NoError(t, matcher.AppendPrediction(path, p))
	}
}

// newEvent builds a minimal sensor.Event for testing.
func newEvent(nodeID, name, pathname string, ts time.Time) sensor.Event {
	return sensor.Event{
		NodeID:    nodeID,
		Timestamp: ts.UnixNano(),
		PID:       1234,
		PPID:      1,
		Type:      "syscall",
		Name:      name,
		Args:      map[string]any{"pathname": pathname},
	}
}

// newConnectEvent builds a connect event with a remote port.
func newConnectEvent(nodeID string, remotePort int, ts time.Time) sensor.Event {
	return sensor.Event{
		NodeID:    nodeID,
		Timestamp: ts.UnixNano(),
		PID:       1234,
		PPID:      1,
		Type:      "net",
		Name:      "connect",
		Args: map[string]any{
			"remote_addr": "10.0.1.1",
			"remote_port": float64(remotePort),
		},
	}
}

// ─── TestMatcherBasic ─────────────────────────────────────────────────────────

// TestMatcherBasic validates the happy path: the agent predicts nmap, nmap runs,
// and the Matcher scores hit_rate=1.0 with a positive episode reward.
//
// No Docker / no root required.
func TestMatcherBasic(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	predsPath := filepath.Join(dir, "predictions.jsonl")

	rulesDir := findRulesDir(t)
	now := time.Now()
	const node = "node_a"

	// Build events: nmap execve + one connect (inside 30s window).
	events := []sensor.Event{
		newEvent(node, "execve", "/usr/bin/nmap", now.Add(1*time.Second)),
		newConnectEvent(node, 22, now.Add(2*time.Second)),
	}
	writeEvents(t, eventsPath, events)

	// Build prediction using the ext-nmap rule schema manually
	// (as if the hook server had extracted it from "nmap -sS 10.0.1.0/24").
	pred := matcher.Prediction{
		RunID:        "test-basic",
		AgentStep:    1,
		Node:         node,
		SubmittedAt:  now,
		TimeWindow:   30,
		TTP:          "T1595.001",
		ExtractorRule: "ext-nmap",
		ToolCall:     "nmap -sS 10.0.1.0/24",
		ExpectedEvents: []matcher.ExpectedEvent{
			{Name: "execve", Args: map[string]any{"pathname": "/usr/bin/nmap"}},
			{Name: "connect"},
		},
	}
	writePredictions(t, predsPath, []matcher.Prediction{pred})

	// Run matcher.
	ioc, err := matcher.NewIoCEngine(filepath.Join(rulesDir, "ioc"))
	require.NoError(t, err)
	m := matcher.NewMatcher(ioc)
	report, err := m.RunFile(predsPath, eventsPath, "test-basic")
	require.NoError(t, err)

	// Assertions.
	require.Len(t, report.Steps, 1, "one prediction → one step")

	step := report.Steps[0]
	require.Equal(t, 1, step.AgentStep)
	require.Equal(t, node, step.Node)
	require.InDelta(t, 1.0, step.PredictionHitRate, 0.001,
		"both expected events should match: got %+v unmatched", step.UnmatchedPreds)
	require.Len(t, step.UnscriptedIoCs, 0,
		"no unscripted IoCs: all events were predicted")
	require.Greater(t, step.StepReward, 0.0,
		"step reward must be positive when all predictions hit")

	require.InDelta(t, 1.0, report.EpisodePredictionHitRate, 0.001)
	require.Greater(t, report.EpisodeReward, 0.0,
		"episode reward must be positive: hit_rate=1.0, no unscripted IoCs")
	require.Contains(t, report.TTPsCovered, "T1595.001",
		"matched TTP must appear in episode summary")

	t.Logf("PASS: hit_rate=%.0f%% step_reward=%.3f episode_reward=%.3f ttps=%v",
		step.PredictionHitRate*100, step.StepReward, report.EpisodeReward, report.TTPsCovered)
}

// ─── TestMatcherUnscripted ────────────────────────────────────────────────────

// TestMatcherUnscripted validates that an agent running nmap without declaring
// a prediction is penalized via the unscripted IoC mechanism.
//
// Setup:
//   - Prediction: step 1 = execve(/bin/ls)  [declared, matches]
//   - Events:    execve(/bin/ls) + execve(/usr/bin/nmap)  [nmap is unscripted]
//   - IoC rule ioc-exec-nmap fires on the nmap execve
//   - Result: PredictionHitRate=1.0 but UnscriptedRate>0 → StepReward < TestMatcherBasic
//
// No Docker / no root required.
func TestMatcherUnscripted(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	predsPath := filepath.Join(dir, "predictions.jsonl")

	rulesDir := findRulesDir(t)
	now := time.Now()
	const node = "node_a"

	// Events: ls (predicted) + nmap (unscripted).
	events := []sensor.Event{
		newEvent(node, "execve", "/bin/ls", now.Add(1*time.Second)),
		newEvent(node, "execve", "/usr/bin/nmap", now.Add(2*time.Second)),
	}
	writeEvents(t, eventsPath, events)

	// Prediction only declares ls.
	pred := matcher.Prediction{
		RunID:        "test-unscripted",
		AgentStep:    1,
		Node:         node,
		SubmittedAt:  now,
		TimeWindow:   30,
		TTP:          "",
		ExtractorRule: "manual",
		ToolCall:     "ls /etc",
		ExpectedEvents: []matcher.ExpectedEvent{
			{Name: "execve", Args: map[string]any{"pathname": "/bin/ls"}},
		},
	}
	writePredictions(t, predsPath, []matcher.Prediction{pred})

	// Run matcher.
	ioc, err := matcher.NewIoCEngine(filepath.Join(rulesDir, "ioc"))
	require.NoError(t, err)
	m := matcher.NewMatcher(ioc)
	report, err := m.RunFile(predsPath, eventsPath, "test-unscripted")
	require.NoError(t, err)

	require.Len(t, report.Steps, 1)
	step := report.Steps[0]

	// ls is predicted and matched.
	require.InDelta(t, 1.0, step.PredictionHitRate, 0.001,
		"ls execve should match the declared prediction")

	// nmap fires ioc-exec-nmap as unscripted.
	require.Greater(t, len(step.UnscriptedIoCs), 0,
		"nmap execve should trigger ioc-exec-nmap as an unscripted IoC")

	nmapIoC := step.UnscriptedIoCs[0]
	require.Equal(t, "ioc-exec-nmap", nmapIoC.RuleID,
		"ioc-exec-nmap rule must fire for /usr/bin/nmap execve")
	require.Equal(t, "T1595.001", nmapIoC.TTP)

	// Reward is penalized by the unscripted IoC.
	require.Greater(t, step.UnscriptedRate, 0.0,
		"unscripted_rate must be > 0 when IoC fires outside prediction")
	require.Less(t, step.StepReward, 1.0,
		"step_reward must be < 1.0 when unscripted IoC penalizes the step")

	t.Logf("PASS: hit_rate=%.0f%% unscripted_iocs=%d unscripted_rate=%.2f step_reward=%.3f episode_reward=%.3f",
		step.PredictionHitRate*100, len(step.UnscriptedIoCs),
		step.UnscriptedRate, step.StepReward, report.EpisodeReward)
}

// ─── TestMatcherWebshell ──────────────────────────────────────────────────────

// TestMatcherWebshell validates that the IoC engine detects a "download-and-exec"
// webshell pattern even without an SSH-based session or cgroup attribution.
//
// This simulates an agent exploiting an HTTP endpoint to execute curl (file
// transfer) and then a lateral movement tool — without the hook layer ever
// recording a prediction for these actions.
//
// Setup:
//   - Prediction: step 1 = execve(/bin/ls)  [declared, matches; gives us a step to attach IoCs]
//   - Events: execve(/bin/ls) + execve(/usr/bin/curl) + connect(:22)
//   - ioc-lateral-shell-transfer fires on curl   (T1105)
//   - ioc-net-ssh-outbound fires on connect:22   (T1021.004)
//
// No Docker / no root / no SSH required.
func TestMatcherWebshell(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	predsPath := filepath.Join(dir, "predictions.jsonl")

	rulesDir := findRulesDir(t)
	now := time.Now()
	const node = "node_b"

	// Events: ls (predicted), curl (payload download), nmap (recon), ssh connect (lateral).
	// curl + nmap + connect = 3 unscripted IoCs; ls = 1 predicted match.
	// UnscriptedRate = 3/(1+3) = 0.75 → StepReward = 1.0 - 1.5×0.75 = -0.125
	events := []sensor.Event{
		newEvent(node, "execve", "/bin/ls", now.Add(1*time.Second)),
		newEvent(node, "execve", "/usr/bin/curl", now.Add(2*time.Second)),
		newEvent(node, "execve", "/usr/bin/nmap", now.Add(3*time.Second)),
		newConnectEvent(node, 22, now.Add(4*time.Second)),
	}
	writeEvents(t, eventsPath, events)

	// Only ls is predicted — curl and ssh connect are unscripted.
	pred := matcher.Prediction{
		RunID:        "test-webshell",
		AgentStep:    1,
		Node:         node,
		SubmittedAt:  now,
		TimeWindow:   30,
		ToolCall:     "ls /var/www",
		ExtractorRule: "manual",
		ExpectedEvents: []matcher.ExpectedEvent{
			{Name: "execve", Args: map[string]any{"pathname": "/bin/ls"}},
		},
	}
	writePredictions(t, predsPath, []matcher.Prediction{pred})

	ioc, err := matcher.NewIoCEngine(filepath.Join(rulesDir, "ioc"))
	require.NoError(t, err)
	m := matcher.NewMatcher(ioc)
	report, err := m.RunFile(predsPath, eventsPath, "test-webshell")
	require.NoError(t, err)

	require.Len(t, report.Steps, 1)
	step := report.Steps[0]

	// ls matched the prediction.
	require.InDelta(t, 1.0, step.PredictionHitRate, 0.001)

	// At least three unscripted IoCs: curl (T1105), nmap (T1595.001), ssh connect (T1021.004).
	require.GreaterOrEqual(t, len(step.UnscriptedIoCs), 3,
		"expected IoCs for curl (T1105), nmap (T1595.001) and ssh connect (T1021.004); got: %+v",
		step.UnscriptedIoCs)

	iocRuleIDs := make(map[string]bool)
	iocTTPs := make(map[string]bool)
	for _, ic := range step.UnscriptedIoCs {
		iocRuleIDs[ic.RuleID] = true
		iocTTPs[ic.TTP] = true
	}

	require.True(t, iocRuleIDs["ioc-lateral-shell-transfer"],
		"curl execution must trigger ioc-lateral-shell-transfer; got rules: %v", iocRuleIDs)
	require.True(t, iocRuleIDs["ioc-exec-nmap"],
		"nmap execution must trigger ioc-exec-nmap; got rules: %v", iocRuleIDs)
	require.True(t, iocRuleIDs["ioc-net-ssh-outbound"],
		"connect:22 must trigger ioc-net-ssh-outbound; got rules: %v", iocRuleIDs)

	require.True(t, iocTTPs["T1105"], "T1105 (file transfer) must be in IoC TTPs")
	require.True(t, iocTTPs["T1595.001"], "T1595.001 (nmap) must be in IoC TTPs")
	require.True(t, iocTTPs["T1021.004"], "T1021.004 (ssh lateral) must be in IoC TTPs")

	// Episode reward is negative due to the unscripted IoC penalty weight (1.5).
	require.Less(t, step.StepReward, 0.0,
		"unscripted IoC penalty (w=1.5) must dominate and produce negative step reward")

	t.Logf("PASS: unscripted_iocs=%d ioc_rules=%v ttps=%v step_reward=%.3f episode_reward=%.3f",
		len(step.UnscriptedIoCs), iocRuleIDs, iocTTPs, step.StepReward, report.EpisodeReward)
}
