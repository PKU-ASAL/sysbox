package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

// leakCanary is a distinctive plaintext passed as an apply input. It must never
// appear in any durably-persisted object.
const leakCanary = "plaintext-canary-must-not-leak"

// TestSensitiveInputNeverPersisted is the global regression tripwire for the
// sensitive-inputs invariant. The point-wise tests in
// sensitive_input_persistence_test.go cover each persistence target in
// isolation; this test drives a real publish+apply with a canary input and then
// sweeps every persisted object reachable through the store, asserting the
// canary appears nowhere. If any future code path persists an apply input (or a
// derived field carrying it), this test is the backstop that catches it.
func TestSensitiveInputNeverPersisted(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	registerDockerAgent(t, s)

	const topology = "cf-leak-sweep"
	rev := publishRevision(t, s, upsertApplyHCL)
	runID := applyUpsertInputs(t, s, topology, rev, map[string]string{"flag": leakCanary})

	// Apply only dispatches a run; it does not itself write state or a health
	// snapshot (the agent does that later). Seed real objects for those two so
	// the sweep reads actual persisted content instead of skipping a "not found".
	// Neither seeded object carries the canary, so the absence assertion is
	// meaningful: it proves the read path returns real bytes, and it would fail
	// if an apply input ever leaked into state or health.
	mgr, err := s.stateManager(topology)
	require.NoError(t, err)
	require.NoError(t, mgr.Save(&state.State{
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_node", "web"),
			Driver:     "docker",
			Attributes: map[string]any{"image": "alpine"},
		}},
	}))
	require.NoError(t, s.saveHealthSnapshot(topology, HealthSnapshot{
		Topology: topology,
		Observed: time.Now().UTC(),
		Health:   controlplane.TopologyHealth{Status: controlplane.ResourceHealthHealthy, Healthy: 1},
		Policy:   SupervisorPolicyObserveOnly,
	}))

	// Sweep runs: the dispatched apply must have produced at least one durable
	// run record, and none may contain the canary.
	runs, err := s.apiStore.LoadRuns(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, runs, "apply must persist a run record")
	for i := range runs {
		raw, err := json.Marshal(runs[i])
		require.NoError(t, err)
		require.NotContains(t, string(raw), leakCanary, "durable run %q must not persist the canary", runs[i].ID)
	}

	// Sweep the specific dispatched run, including its inputs field.
	run, err := s.apiStore.GetRun(ctx, runID)
	require.NoError(t, err)
	require.Nil(t, run.Inputs, "durable run record must not persist sensitive inputs")
	rawRun, err := json.Marshal(run)
	require.NoError(t, err)
	require.NotContains(t, string(rawRun), leakCanary, "durable run %q serialization must not persist the canary", runID)

	// Sweep agent commands: the run_assigned dispatch must be present and clean.
	commands, err := s.apiStore.ListAgentCommands(ctx, "host-a")
	require.NoError(t, err)
	require.NotEmpty(t, commands, "apply must dispatch an agent command")
	for i := range commands {
		raw, err := json.Marshal(commands[i])
		require.NoError(t, err)
		require.NotContains(t, string(raw), leakCanary, "agent command %q must not persist the canary", commands[i].ID)
	}

	// Sweep state.
	st, err := s.workspaceService().LoadState(topology)
	require.NoError(t, err)
	require.NotEmpty(t, st.Resources, "seeded state must be readable with real resources")
	rawState, err := st.Marshal()
	require.NoError(t, err)
	require.NotContains(t, string(rawState), leakCanary, "state must not persist the canary")

	// Sweep health snapshot.
	snap, err := s.apiStore.LoadHealth(ctx, topology)
	require.NoError(t, err)
	require.NotNil(t, snap)
	rawHealth, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(rawHealth), leakCanary, "health snapshot must not persist the canary")
}
