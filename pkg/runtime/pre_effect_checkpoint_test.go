package runtime

import (
	"context"
	"errors"
	"github.com/docker/docker/api/types/filters"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

type failStepCheckpoint struct{}

func (failStepCheckpoint) SaveCheckpoint(_ context.Context, _, _ string, cp OperationCheckpoint) error {
	if len(cp.Steps) > 0 {
		return errors.New("checkpoint storage unavailable")
	}
	return nil
}
func TestCheckpointFailurePreventsExternalEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	recorder := NewStoreRecorder(NewFileRecorder(path, "run", "topology"), failStepCheckpoint{}, "topology", "run", path)
	require.NoError(t, recorder.Begin("apply", nil))
	exec := &Executor{recorder: recorder}
	called := false
	err := exec.recordSubstep(0, "guest_exec", nil, func() error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called, "effect cannot precede durable intent checkpoint")
}

func TestLabelRecoveryRefusesAmbiguousMatches(t *testing.T) {
	labels := map[string]string{LabelManaged: "true", LabelTopology: "topo", LabelResource: "node.web", LabelRunID: "run"}
	id, err := findDockerObjectByLabels(context.Background(), labels, func(filters.Args) ([]string, error) { return []string{"first", "second"}, nil })
	require.Error(t, err)
	require.Empty(t, id)
}
func TestLabelRecoveryRefusesUnscopedLookup(t *testing.T) {
	called := false
	_, err := findDockerObjectByLabels(context.Background(), map[string]string{LabelManaged: "true"}, func(filters.Args) ([]string, error) { called = true; return nil, nil })
	require.Error(t, err)
	require.False(t, called)
}

func TestResumeBlocksUnconfirmedGuestEffect(t *testing.T) {
	cp := OperationCheckpoint{Steps: []OperationStep{{Kind: "subaction", Phase: "guest_exec", Status: OperationStarted}}}
	require.ErrorContains(t, CheckpointResumeSafety(cp), "unconfirmed effect")
	cp.Steps[0].Status = OperationDone
	require.NoError(t, CheckpointResumeSafety(cp))
}

type interruptedReceiptStore struct {
	failed  bool
	durable OperationCheckpoint
}

func (s *interruptedReceiptStore) SaveCheckpoint(_ context.Context, _, _ string, cp OperationCheckpoint) error {
	if s.failed {
		return errors.New("lost checkpoint connection after provider effect")
	}
	s.durable = cp
	return nil
}
func TestProviderSuccessBeforeReceiptLeavesAccountedBlockedOrphan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	store := &interruptedReceiptStore{}
	recorder := NewStoreRecorder(NewFileRecorder(path, "run", "topology"), store, "topology", "run", path)
	require.NoError(t, recorder.Begin("apply", nil))
	parent := recorder.StepStart("sysbox_node.web", controlplane.PlanActionCreate)
	labels := map[string]string{LabelManaged: "true", LabelTopology: "topology", LabelResource: "sysbox_node.web", LabelRunID: "run"}
	recorder.StepExternal(parent, "docker", "", labels)
	exec := &Executor{recorder: recorder}
	var createdLabels map[string]string
	err := exec.recordSubstep(parent, "create_node", nil, func() error {
		createdLabels = labels // successful provider creation; no external-ID receipt yet
		store.failed = true
		return nil
	})
	require.ErrorContains(t, err, "effect receipt checkpoint")
	require.Equal(t, createdLabels, store.durable.Steps[0].Labels, "pre-effect durable identity accounts for the orphan")
	require.Empty(t, store.durable.Steps[0].ExternalID)
	require.ErrorContains(t, CheckpointResumeSafety(store.durable), "unconfirmed effect")
	repeated := false
	require.Error(t, exec.recordSubstep(parent, "guest_exec", nil, func() error { repeated = true; return nil }))
	require.False(t, repeated, "incomplete provider receipt cannot authorize guest execution or fresh replay")
}
