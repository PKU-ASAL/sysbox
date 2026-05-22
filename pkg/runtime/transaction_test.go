package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileRecorderPersistsPlanLeaseAndStateSerials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.checkpoint.json")
	rec := NewFileRecorder(path, "run-1", "mixed")
	rec.SetLeaseOwner("sysbox-api:apply:run-1")
	rec.SetStateSerialBefore(4)

	plan := &Plan{Actions: []PlanAction{{
		Resource: "sysbox_node.web",
		Type:     "sysbox_node",
		Name:     "web",
		Action:   PlanActionCreate,
	}}}
	require.NoError(t, rec.Begin("apply", plan))
	step := rec.StepStartKind("state", "state", PlanActionUpdate)
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

	parent := rec.StepStart("sysbox_node.vm", PlanActionCreate)
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
