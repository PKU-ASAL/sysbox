package api

import (
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"os"

	"github.com/oslab/sysbox/pkg/runtime"
)

type RunActionLog struct {
	RunID     string                    `json:"run_id"`
	Topology  string                    `json:"topology,omitempty"`
	Operation string                    `json:"operation,omitempty"`
	Status    runtime.OperationStatus   `json:"status,omitempty"`
	Plan      []controlplane.PlanAction `json:"plan,omitempty"`
	Actions   []runtime.OperationStep   `json:"actions"`
}

func loadRunActionLog(path string) (*RunActionLog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("checkpoint not found")
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return runActionLogFromCheckpoint(cp), nil
}

func runActionLogFromCheckpoint(cp runtime.OperationCheckpoint) *RunActionLog {
	return &RunActionLog{
		RunID:     cp.RunID,
		Topology:  cp.Topology,
		Operation: cp.Operation,
		Status:    cp.Status,
		Plan:      cp.Plan,
		Actions:   cp.Steps,
	}
}
