package agentexec

import (
	"context"
	"errors"
	"io"
	"os"
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
	for _, phase := range []string{"intent", "patch"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "state.json")
			kernelPath := filepath.Join(dir, "kernel")
			require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0600))
			st := &state.State{Version: state.SchemaVersion}
			st.AddResource(state.Resource{Address: address.Resource("sysbox_kernel", "test"), Attributes: state.MustAttributes(map[string]any{"path": kernelPath})})
			saveLocalTestState(t, statePath, "lab", st)
			want := errors.New("checkpoint rejected " + phase)
			bridge := NewLocalBridge(LocalOptions{Topology: "lab", StatePath: statePath, RunsDir: dir, Log: io.Discard, CheckpointStore: rejectJournalPhase{phase: phase, err: want}})
			run := &controlplane.Run{ID: "run", Topology: "lab", Op: "destroy"}
			NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
			require.Equal(t, controlplane.RunFailed, run.Status)
			require.Contains(t, run.Err, want.Error())
			mgr, err := bridge.StateManager("lab")
			require.NoError(t, err)
			stored, err := mgr.Load()
			require.NoError(t, err)
			if phase == "intent" {
				require.Len(t, stored.Resources, 1)
			} else {
				require.Empty(t, stored.Resources, "atomic action outcome already committed before checkpoint projection failed")
			}
		})
	}
}
