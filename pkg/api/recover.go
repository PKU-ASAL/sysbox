package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type RecoverReport struct {
	RunID     string          `json:"run_id"`
	Topology  string          `json:"topology,omitempty"`
	Recovered []RecoverAction `json:"recovered,omitempty"`
	Skipped   []RecoverAction `json:"skipped,omitempty"`
}

type RecoverAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func recoverCheckpoint(ctx context.Context, store apiStore, topology, runID string, mgr *state.Manager, owner string) (*RecoverReport, error) {
	cpPtr, err := store.LoadCheckpoint(ctx, topology, runID)
	if err != nil {
		return nil, err
	}
	cp := *cpPtr

	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	report := &RecoverReport{RunID: cp.RunID, Topology: cp.Topology}
	patchRecovered := false
	for _, step := range cp.Steps {
		if !recoverCandidate(step) {
			continue
		}
		if patchRecovered && step.StateResource != nil && st.FindResource(step.StateResource.Type, step.StateResource.Name) != nil {
			continue
		}
		result, ok := runtime.RecoverCheckpointResource(ctx, st, step)
		if !ok {
			continue
		}
		action := recoverActionFromRuntime(result)
		if actionRecovered(action) {
			report.Recovered = append(report.Recovered, action)
		} else {
			report.Skipped = append(report.Skipped, action)
		}
	}
	if len(report.Recovered) > 0 {
		st.RunID = cp.RunID
		if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: owner}); err != nil {
			return nil, fmt.Errorf("save recovered state: %w", err)
		}
	}
	return report, nil
}

func actionRecovered(action RecoverAction) bool {
	return action.Status == "recovered" ||
		action.Status == "recovered_adopted" ||
		action.Status == "recovered_not_running" ||
		action.Status == "replayed_state_patch"
}

func recoverCandidate(step runtime.OperationStep) bool {
	return step.Kind == "resource" &&
		step.Status == runtime.OperationDone &&
		!step.StateRecorded &&
		step.StateResource != nil &&
		runtime.SupportsCheckpointRecover(step)
}

func recoverActionFromRuntime(result runtime.CheckpointRecoverResult) RecoverAction {
	return RecoverAction{
		Resource:   result.Resource,
		ExternalID: result.ExternalID,
		Status:     result.Status,
		Error:      result.Error,
	}
}
