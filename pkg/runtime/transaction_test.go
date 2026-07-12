package runtime

import (
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
)

func TestFileRecorderPersistsPlanLeaseAndStateSerials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.checkpoint.json")
	rec := NewFileRecorder(path, "run-1", "mixed")
	rec.SetLeaseOwner("sysbox-api:apply:run-1")
	rec.SetStateSerialBefore(4)

	plan := &Plan{Actions: []controlplane.PlanAction{{
		Resource: "sysbox_node.web",
		Type:     "sysbox_node",
		Name:     "web",
		Action:   controlplane.PlanActionCreate,
	}}}
	require.NoError(t, rec.Begin("apply", plan))
	step := rec.StepStartKind("state", "state", controlplane.PlanActionUpdate)
	rec.StepFailed(step, errors.New("cas conflict"))
	rec.SetStateSerialAfter(5)
	rec.Finish(errors.New("failed"))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var cp OperationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &cp))
	require.Equal(t, "run-1", cp.RunID)
	require.Equal(t, "sysbox-api:apply:run-1", cp.LeaseOwner)
	require.Equal(t, int64(4), cp.StateSerialBefore)
	require.Equal(t, int64(5), cp.StateSerialAfter)
	require.Len(t, cp.Plan, 1)
	require.Len(t, cp.Steps, 1)
	require.Equal(t, "state", cp.Steps[0].Kind)
	require.Equal(t, OperationFailed, cp.Status)
	require.Equal(t, OperationFailed, cp.Steps[0].Status)
}

func TestFileRecorderPersistsSubsteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.checkpoint.json")
	rec := NewFileRecorder(path, "run-1", "mixed")
	require.NoError(t, rec.Begin("apply", nil))

	parent := rec.StepStart("sysbox_node.vm", controlplane.PlanActionCreate)
	child := rec.SubstepStart(parent, "create_resource", map[string]any{"resource": "sysbox_node.vm"})
	rec.StepDone(child)
	rec.StepDone(parent)
	rec.Finish(nil)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var cp OperationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &cp))
	require.Len(t, cp.Steps, 2)
	require.Equal(t, "resource", cp.Steps[0].Kind)
	require.Equal(t, "subaction", cp.Steps[1].Kind)
	require.Equal(t, "create_resource", cp.Steps[1].Phase)
	require.Equal(t, parent, cp.Steps[1].Parent)
	require.Equal(t, "sysbox_node.vm", cp.Steps[1].Details["resource"])
	require.Equal(t, OperationDone, cp.Steps[1].Status)
}

func TestFileRecorderPersistsStatePatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.checkpoint.json")
	rec := NewFileRecorder(path, "run-1", "mixed")
	require.NoError(t, rec.Begin("apply", nil))

	step := rec.StepStart("sysbox_node.web", controlplane.PlanActionCreate)
	stateLog := StateResourceLog{
		Type:     "sysbox_node",
		Name:     "web",
		Provider: "docker",
		Instance: map[string]any{"container_id": "abc"},
	}
	rec.StepStateResource(step, stateLog)
	rec.StepStatePatch(step, StatePatchUpsert, &stateLog)
	rec.StepDone(step)
	rec.StepStateRecorded(step)
	rec.Finish(nil)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var cp OperationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &cp))
	require.Len(t, cp.StatePatches, 1)
	require.Equal(t, StatePatchUpsert, cp.StatePatches[0].Op)
	require.Equal(t, "sysbox_node.web", cp.StatePatches[0].Resource)
	require.True(t, cp.StatePatches[0].Recorded)
	require.Equal(t, "abc", cp.StatePatches[0].State.Instance["container_id"])
}

func TestApplyStatePatchUpsertAndDelete(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	patch := StatePatch{
		Resource: "sysbox_node.web",
		Action:   controlplane.PlanActionCreate,
		Op:       StatePatchUpsert,
		State: &StateResourceLog{
			Type:     "sysbox_node",
			Name:     "web",
			Provider: "docker",
			Instance: map[string]any{"container_id": "abc"},
		},
	}
	require.True(t, ApplyStatePatch(st, patch))
	require.Equal(t, "abc", st.FindResource(address.Resource("sysbox_node", "web")).ContainerID())

	patch.State.Instance["container_id"] = "def"
	require.True(t, ApplyStatePatch(st, patch))
	require.Equal(t, "def", st.FindResource(address.Resource("sysbox_node", "web")).ContainerID())
	require.Len(t, st.Resources, 1)

	require.True(t, ApplyStatePatch(st, StatePatch{
		Resource: "sysbox_node.web",
		Action:   controlplane.PlanActionDelete,
		Op:       StatePatchDelete,
	}))
	require.Nil(t, st.FindResource(address.Resource("sysbox_node", "web")))
}
