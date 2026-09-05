package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// Sensitive apply inputs are transient: the agent binds var.<name> from the
// run it receives (the claim response), but the plaintext must never survive in
// the durable run record. The in-memory run and the claim response carry the
// input; the store record must not.
func TestSensitiveApplyInputsNotDurablyPersisted(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	const canary = "plaintext-canary-must-not-persist"
	rev := publishRevision(t, s, upsertApplyHCL)
	runID := applyUpsertInputs(t, s, "cf-sensitive", rev, map[string]string{"flag": canary})

	// Transient: the in-memory run retains the input so the agent can bind it.
	mem, ok := s.jobs.runs[runID]
	require.True(t, ok, "in-memory run must exist after apply")
	require.Equal(t, canary, mem.Inputs["flag"])

	// Durable: the store record must not contain the input, and its serialized
	// form must not contain the plaintext.
	durable, err := s.jobs.store.GetRun(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, durable)
	require.Nil(t, durable.Inputs, "durable run record must not persist sensitive inputs")

	raw, err := json.Marshal(durable)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary, "durable run serialization must not contain the canary plaintext")

	// The durable record's op/status must still be intact (strip only inputs).
	require.Equal(t, controlplane.RunAssigned, durable.Status)
	require.Equal(t, "apply", durable.Op)

	// Durable: the dispatched run_assigned command must not carry the input
	// either — the agent binds var.<name> from the claim response, not from the
	// command's Run.
	commands, err := s.apiStore.ListAgentCommands(context.Background(), "host-a")
	require.NoError(t, err)
	require.Len(t, commands, 1)
	require.Nil(t, commands[0].Run.Inputs, "run_assigned command must not persist sensitive inputs")
	commandRaw, err := json.Marshal(commands[0])
	require.NoError(t, err)
	require.NotContains(t, string(commandRaw), canary, "run_assigned command serialization must not contain the canary plaintext")

	// Transient: the claim response the agent executes must still carry the
	// input even though the durable record is stripped.
	claimed, err := s.jobs.claim(runID, "host-a")
	require.NoError(t, err)
	require.Equal(t, canary, claimed.Inputs["flag"], "agent claim response must retain the transient input")
}
