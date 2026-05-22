package api

import (
	"context"

	"github.com/oslab/sysbox/pkg/runtime"
)

type CleanupReport struct {
	RunID      string          `json:"run_id"`
	Topology   string          `json:"topology,omitempty"`
	Containers []CleanupAction `json:"containers,omitempty"`
	Networks   []CleanupAction `json:"networks,omitempty"`
	MicroVMs   []CleanupAction `json:"microvms,omitempty"`
}

type CleanupAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Class      string `json:"-"`
}

func cleanupCheckpoint(ctx context.Context, store apiStore, topology, runID string) (*CleanupReport, error) {
	cp, err := store.LoadCheckpoint(ctx, topology, runID)
	if err != nil {
		return nil, err
	}

	report := &CleanupReport{RunID: cp.RunID, Topology: cp.Topology}
	for i := len(cp.Steps) - 1; i >= 0; i-- {
		step := cp.Steps[i]
		if !cleanupCandidate(step) {
			continue
		}
		result, ok := runtime.CleanupCheckpointResource(ctx, step)
		if !ok {
			continue
		}
		appendCleanupAction(report, cleanupActionFromRuntime(result))
	}
	return report, nil
}

func cleanupCandidate(step runtime.OperationStep) bool {
	return step.Kind == "resource" &&
		step.Status == runtime.OperationDone &&
		!step.StateRecorded &&
		runtime.SupportsCheckpointCleanup(step)
}

func appendCleanupAction(report *CleanupReport, action CleanupAction) {
	switch action.Class {
	case string(runtime.CheckpointCleanupNetwork):
		report.Networks = append(report.Networks, action)
	case string(runtime.CheckpointCleanupMicroVM):
		report.MicroVMs = append(report.MicroVMs, action)
	default:
		report.Containers = append(report.Containers, action)
	}
}

func cleanupActionFromRuntime(result runtime.CheckpointCleanupResult) CleanupAction {
	return CleanupAction{
		Resource:   result.Resource,
		ExternalID: result.ExternalID,
		Status:     result.Status,
		Error:      result.Error,
		Class:      string(result.Class),
	}
}
