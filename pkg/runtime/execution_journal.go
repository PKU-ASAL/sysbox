package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/state"
)

type CheckpointStore interface {
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint OperationCheckpoint) error
}

type StoreRecorder struct {
	OperationRecorder
	store    CheckpointStore
	ctx      context.Context
	topology string
	runID    string
	path     string
}

func NewStoreRecorder(inner OperationRecorder, store CheckpointStore, topology, runID, path string) *StoreRecorder {
	return &StoreRecorder{OperationRecorder: inner, store: store, ctx: context.Background(), topology: topology, runID: runID, path: path}
}

func (r *StoreRecorder) WithContext(ctx context.Context) *StoreRecorder {
	if ctx == nil {
		ctx = context.Background()
	}
	r.ctx = ctx
	return r
}

func (r *StoreRecorder) Begin(operation string, plan *Plan) error {
	if err := r.OperationRecorder.Begin(operation, plan); err != nil {
		return err
	}
	r.persist()
	return nil
}

func (r *StoreRecorder) StepStart(resource string, action PlanActionType) int {
	idx := r.OperationRecorder.StepStart(resource, action)
	r.persist()
	return idx
}

func (r *StoreRecorder) StepDone(index int) {
	r.OperationRecorder.StepDone(index)
	r.persist()
}

func (r *StoreRecorder) StepFailed(index int, err error) {
	r.OperationRecorder.StepFailed(index, err)
	r.persist()
}

func (r *StoreRecorder) Finish(err error) {
	r.OperationRecorder.Finish(err)
	r.persist()
}

func (r *StoreRecorder) SetLeaseOwner(owner string) {
	r.OperationRecorder.SetLeaseOwner(owner)
	r.persist()
}

func (r *StoreRecorder) SetStateSerialBefore(serial int64) {
	r.OperationRecorder.SetStateSerialBefore(serial)
	r.persist()
}

func (r *StoreRecorder) SetStateSerialAfter(serial int64) {
	r.OperationRecorder.SetStateSerialAfter(serial)
	r.persist()
}

func (r *StoreRecorder) StepStartKind(kind, resource string, action PlanActionType) int {
	idx := r.OperationRecorder.StepStartKind(kind, resource, action)
	r.persist()
	return idx
}

func (r *StoreRecorder) StepExternal(index int, provider, externalID string, labels map[string]string) {
	r.OperationRecorder.StepExternal(index, provider, externalID, labels)
	r.persist()
}

func (r *StoreRecorder) StepStateResource(index int, resource StateResourceLog) {
	r.OperationRecorder.StepStateResource(index, resource)
	r.persist()
}

func (r *StoreRecorder) StepStatePatch(index int, op StatePatchOp, resource *StateResourceLog) {
	r.OperationRecorder.StepStatePatch(index, op, resource)
	r.persist()
}

func (r *StoreRecorder) StepStateRecorded(index int) {
	r.OperationRecorder.StepStateRecorded(index)
	r.persist()
}

func (r *StoreRecorder) MarkResourceStateRecorded() {
	r.OperationRecorder.MarkResourceStateRecorded()
	r.persist()
}

func (r *StoreRecorder) SubstepStart(parent int, phase string, details map[string]any) int {
	idx := r.OperationRecorder.SubstepStart(parent, phase, details)
	r.persist()
	return idx
}

func (r *StoreRecorder) persist() {
	if r.store == nil || r.path == "" {
		return
	}
	cp, err := LoadCheckpointFile(r.path)
	if err != nil {
		return
	}
	if err := r.store.SaveCheckpoint(r.ctx, r.topology, r.runID, *cp); err != nil {
		fmt.Fprintf(os.Stderr, "[runtime] persist checkpoint: %v\n", err)
	}
}

type StatePatchManagerSink struct {
	Manager *state.Manager
	State   *state.State
	Owner   string
}

func (s *StatePatchManagerSink) ApplyStatePatch(ctx context.Context, patch StatePatch) error {
	if s == nil || s.Manager == nil || s.State == nil {
		return nil
	}
	ApplyStatePatch(s.State, patch)
	return s.Manager.SaveWithLease(ctx, s.State, state.LockOptions{Owner: s.Owner})
}

func LoadCheckpointFile(path string) (*OperationCheckpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return &cp, nil
}
