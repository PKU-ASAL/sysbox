package api

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

type JournalReplayReport struct {
	Applied []RecoverAction `json:"applied,omitempty"`
	Skipped []RecoverAction `json:"skipped,omitempty"`
}

func replayCheckpointJournal(ctx context.Context, store apiStore, topology, runID string, mgr *state.Manager, owner string) (*JournalReplayReport, error) {
	cpPtr, err := store.LoadCheckpoint(ctx, topology, runID)
	if err != nil {
		return nil, err
	}
	cp := *cpPtr
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
		st.Lineage = cp.RunID
		if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: owner}); err != nil {
			return nil, fmt.Errorf("save replayed state patches: %w", err)
		}
		runtime.MarkStatePatchesRecorded(&cp, recorded)
		if err := store.SaveCheckpoint(ctx, topology, runID, cp); err != nil {
			return nil, fmt.Errorf("mark replayed state patches recorded: %w", err)
		}
	}
	return report, nil
}

func reconcileCheckpointJournal(ctx context.Context, store apiStore, topology, runID string, mgr *state.Manager, owner string) (*RecoverReport, error) {
	if err := mgr.CheckMutationSafety(); err != nil {
		return nil, err
	}
	journal, err := replayCheckpointJournal(ctx, store, topology, runID, mgr, owner)
	if err != nil {
		return nil, err
	}
	recovered, err := recoverCheckpoint(ctx, store, topology, runID, mgr, owner)
	if err != nil {
		return nil, err
	}
	if journal != nil {
		recovered.Recovered = append(journal.Applied, recovered.Recovered...)
		recovered.Skipped = append(journal.Skipped, recovered.Skipped...)
	}
	return recovered, nil
}
