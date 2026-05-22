package api

import (
	"context"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/runtime"
)

type storeRecorder struct {
	runtime.OperationRecorder
	store    apiStore
	topology string
	runID    string
	path     string
}

func newStoreRecorder(inner runtime.OperationRecorder, store apiStore, topology, runID, path string) *storeRecorder {
	return &storeRecorder{OperationRecorder: inner, store: store, topology: topology, runID: runID, path: path}
}

func (r *storeRecorder) Begin(operation string, plan *runtime.Plan) error {
	if err := r.OperationRecorder.Begin(operation, plan); err != nil {
		return err
	}
	r.persist()
	return nil
}

func (r *storeRecorder) StepStart(resource string, action runtime.PlanActionType) int {
	idx := r.OperationRecorder.StepStart(resource, action)
	r.persist()
	return idx
}

func (r *storeRecorder) StepDone(index int) {
	r.OperationRecorder.StepDone(index)
	r.persist()
}

func (r *storeRecorder) StepFailed(index int, err error) {
	r.OperationRecorder.StepFailed(index, err)
	r.persist()
}

func (r *storeRecorder) Finish(err error) {
	r.OperationRecorder.Finish(err)
	r.persist()
}

func (r *storeRecorder) SetLeaseOwner(owner string) {
	r.OperationRecorder.SetLeaseOwner(owner)
	r.persist()
}

func (r *storeRecorder) SetStateSerialBefore(serial int64) {
	r.OperationRecorder.SetStateSerialBefore(serial)
	r.persist()
}

func (r *storeRecorder) SetStateSerialAfter(serial int64) {
	r.OperationRecorder.SetStateSerialAfter(serial)
	r.persist()
}

func (r *storeRecorder) StepStartKind(kind, resource string, action runtime.PlanActionType) int {
	idx := r.OperationRecorder.StepStartKind(kind, resource, action)
	r.persist()
	return idx
}

func (r *storeRecorder) StepExternal(index int, provider, externalID string, labels map[string]string) {
	r.OperationRecorder.StepExternal(index, provider, externalID, labels)
	r.persist()
}

func (r *storeRecorder) StepStateResource(index int, resource runtime.StateResourceLog) {
	r.OperationRecorder.StepStateResource(index, resource)
	r.persist()
}

func (r *storeRecorder) StepStatePatch(index int, op runtime.StatePatchOp, resource *runtime.StateResourceLog) {
	r.OperationRecorder.StepStatePatch(index, op, resource)
	r.persist()
}

func (r *storeRecorder) StepStateRecorded(index int) {
	r.OperationRecorder.StepStateRecorded(index)
	r.persist()
}

func (r *storeRecorder) MarkResourceStateRecorded() {
	r.OperationRecorder.MarkResourceStateRecorded()
	r.persist()
}

func (r *storeRecorder) SubstepStart(parent int, phase string, details map[string]any) int {
	idx := r.OperationRecorder.SubstepStart(parent, phase, details)
	r.persist()
	return idx
}

func (r *storeRecorder) persist() {
	if r.store == nil || r.path == "" {
		return
	}
	cp, err := loadCheckpointFile(r.path)
	if err != nil {
		return
	}
	if err := r.store.SaveCheckpoint(context.Background(), r.topology, r.runID, *cp); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist checkpoint: %v\n", err)
	}
}
