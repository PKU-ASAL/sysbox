package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type statePatchSink struct {
	mgr   *state.Manager
	state *state.State
	owner string
}

func (s *statePatchSink) ApplyStatePatch(ctx context.Context, patch runtime.StatePatch) error {
	if s == nil || s.mgr == nil || s.state == nil {
		return nil
	}
	runtime.ApplyStatePatch(s.state, patch)
	return s.mgr.SaveWithLease(ctx, s.state, state.LockOptions{Owner: s.owner})
}

type JournalReplayReport struct {
	Applied []RecoverAction `json:"applied,omitempty"`
	Skipped []RecoverAction `json:"skipped,omitempty"`
}

func replayCheckpointJournal(ctx context.Context, checkpointPath string, mgr *state.Manager, owner string) (*JournalReplayReport, error) {
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
	report := &JournalReplayReport{}
	changed := false
	recorded := map[int]bool{}
	for _, patch := range runtime.UnrecordedStatePatches(cp) {
		action := RecoverAction{Resource: patch.Resource, Status: "replayed_state_patch"}
		if patch.State != nil {
			if id, _ := patch.State.Instance["container_id"].(string); id != "" {
				action.ExternalID = id
			}
			if id, _ := patch.State.Instance["docker_network_id"].(string); id != "" {
				action.ExternalID = id
			}
		}
		if runtime.ApplyStatePatch(st, patch) {
			report.Applied = append(report.Applied, action)
			changed = true
			recorded[patch.Index] = true
		} else {
			action.Status = "skipped_state_patch"
			report.Skipped = append(report.Skipped, action)
		}
	}
	if changed {
		st.RunID = cp.RunID
		if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: owner}); err != nil {
			return nil, fmt.Errorf("save replayed state patches: %w", err)
		}
		runtime.MarkStatePatchesRecorded(&cp, recorded)
		if err := runtime.WriteCheckpoint(checkpointPath, cp); err != nil {
			return nil, fmt.Errorf("mark replayed state patches recorded: %w", err)
		}
	}
	return report, nil
}

func reconcileCheckpointJournal(ctx context.Context, checkpointPath string, mgr *state.Manager, owner string) (*RecoverReport, error) {
	journal, err := replayCheckpointJournal(ctx, checkpointPath, mgr, owner)
	if err != nil {
		return nil, err
	}
	recovered, err := recoverCheckpoint(ctx, checkpointPath, mgr, owner)
	if err != nil {
		return nil, err
	}
	if journal != nil {
		recovered.Recovered = append(journal.Applied, recovered.Recovered...)
		recovered.Skipped = append(journal.Skipped, recovered.Skipped...)
	}
	return recovered, nil
}
