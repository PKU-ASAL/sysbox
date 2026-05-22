package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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

func recoverCheckpoint(ctx context.Context, checkpointPath string, mgr *state.Manager, owner string) (*RecoverReport, error) {
	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}

	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	report := &RecoverReport{RunID: cp.RunID, Topology: cp.Topology}
	patchRecovered := false
	for _, patch := range cp.StatePatches {
		if !recoverPatchCandidate(patch) {
			continue
		}
		action := recoverStatePatch(st, patch)
		if actionRecovered(action) {
			patchRecovered = true
			report.Recovered = append(report.Recovered, action)
		} else {
			report.Skipped = append(report.Skipped, action)
		}
	}
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

func recoverPatchCandidate(patch runtime.StatePatch) bool {
	return !patch.Recorded && patch.State != nil && patch.Op == runtime.StatePatchUpsert
}

func recoverStatePatch(st *state.State, patch runtime.StatePatch) RecoverAction {
	action := RecoverAction{Resource: patch.Resource, Status: "recovered_from_patch"}
	rec := patch.State
	if rec == nil {
		action.Status = "missing_state_patch"
		return action
	}
	if existing := st.FindResource(rec.Type, rec.Name); existing != nil {
		action.Status = "already_in_state"
		return action
	}
	runtime.AdoptStateResource(st, *rec, "")
	return action
}

func actionRecovered(action RecoverAction) bool {
	return action.Status == "recovered" ||
		action.Status == "recovered_adopted" ||
		action.Status == "recovered_not_running" ||
		action.Status == "recovered_from_patch"
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
