package agentexec

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type countingStateBackend struct {
	*state.LocalBackend
	saves int
}

func (b *countingStateBackend) Save(ctx context.Context, raw []byte) error {
	b.saves++
	return b.LocalBackend.Save(ctx, raw)
}

type journalBarrierBridge struct {
	*LocalBridge
	manager *state.Manager
}

func (b *journalBarrierBridge) StateManager(string) (*state.Manager, error) { return b.manager, nil }

type rejectJournalPhase struct {
	phase string
	err   error
}

func (s rejectJournalPhase) SaveCheckpoint(_ context.Context, _, _ string, cp runtime.OperationCheckpoint) error {
	if s.phase == "patch" && len(cp.StatePatches) > 0 {
		return s.err
	}
	for _, step := range cp.Steps {
		if s.phase == "intent" || step.Kind == s.phase {
			return s.err
		}
	}
	return nil
}

func TestAgentDoesNotSaveStateAfterJournalFailure(t *testing.T) {
	for _, phase := range []string{"intent", "patch", "state"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			backend := &countingStateBackend{LocalBackend: &state.LocalBackend{Path: filepath.Join(dir, "state.json")}}
			manager := state.NewManagerWithBackend(backend)
			st := &state.State{Version: state.SchemaVersion}
			st.AddResource(state.Resource{Address: address.Resource("sysbox_kernel", "test")})
			require.NoError(t, manager.Save(st))
			backend.saves = 0
			want := errors.New("checkpoint rejected " + phase)
			bridge := &journalBarrierBridge{manager: manager, LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", RunsDir: dir, Log: io.Discard, CheckpointStore: rejectJournalPhase{phase: phase, err: want}})}
			run := &controlplane.Run{ID: "run", Topology: "lab", Op: "destroy"}
			NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
			require.Equal(t, controlplane.RunFailed, run.Status)
			require.Contains(t, run.Err, want.Error())
			if phase == "state" {
				require.Equal(t, 1, backend.saves, "only the already journaled resource patch may commit")
			} else {
				require.Zero(t, backend.saves, "agent must not bypass a rejected journal with a whole-state save")
			}
		})
	}
}
