package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/address"
)

type RunExecutionRequest struct {
	RunID          string `json:"run_id"`
	Topology       string `json:"topology"`
	TopologyUID    string `json:"topology_uid"`
	Revision       string `json:"revision"`
	ActionID       string `json:"action_id"`
	ResourceUID    string `json:"resource_uid"`
	Generation     int64  `json:"generation"`
	Action         string `json:"action"`
	Target         string `json:"target"`
	ExpectedSerial int64  `json:"expected_serial"`
}

// IsRunExecutionOperation identifies lifecycle operations that require a
// host-signed Run result, including child-task stop evidence when applicable.
func IsRunExecutionOperation(operation string) bool {
	return operation == "run_apply" || operation == "run_destroy" || operation == "run_reset"
}

func (r RunExecutionRequest) Validate() error {
	if r.RunID == "" || r.Topology == "" || r.TopologyUID == "" || r.Revision == "" || r.ActionID == "" || r.ExpectedSerial < 1 {
		return fmt.Errorf("run execution request incomplete")
	}
	if !IsRunExecutionOperation("run_" + r.Action) {
		return fmt.Errorf("run action unsupported")
	}
	if strings.ContainsAny(r.RunID+r.Topology+r.TopologyUID+r.Revision+r.ActionID+r.ResourceUID, "/\\\x00") {
		return fmt.Errorf("run execution identity unsafe")
	}
	if r.ResourceUID == "" {
		if r.Generation != 0 || r.Target != "" {
			return fmt.Errorf("topology run cannot carry a resource target")
		}
	} else {
		if r.Generation < 1 || r.Target == "" {
			return fmt.Errorf("resource run requires generation and target")
		}
		if _, err := address.Parse(r.Target); err != nil {
			return fmt.Errorf("run target invalid: %w", err)
		}
	}
	return nil
}
func (r RunExecutionRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func FromRunCommand(cmd AgentCommand, topologyUID string, serial int64) (RunExecutionRequest, error) {
	if cmd.Type != "run_assigned" || cmd.Run == nil || cmd.Run.ID == "" || cmd.Run.Topology == "" || cmd.Run.Revision == "" || topologyUID == "" || cmd.TopologyUID == "" || cmd.TopologyUID != topologyUID || cmd.ActionID == "" || serial < 1 {
		return RunExecutionRequest{}, fmt.Errorf("run command lacks single-resource host proof")
	}
	action := cmd.Run.Op
	if action == "" {
		action = cmd.Run.Operation
	}
	if !IsRunExecutionOperation("run_" + action) {
		return RunExecutionRequest{}, fmt.Errorf("unsupported lifecycle Run operation")
	}
	target := cmd.Run.Target
	if cmd.ResourceUID == "" {
		if cmd.Generation != 0 || target != "" {
			return RunExecutionRequest{}, fmt.Errorf("topology run carries resource proof")
		}
	} else {
		if cmd.Generation < 1 || target == "" {
			return RunExecutionRequest{}, fmt.Errorf("resource run lacks target proof")
		}
		digestTarget, err := address.Parse(target)
		if err != nil {
			return RunExecutionRequest{}, err
		}
		target = digestTarget.String()
	}
	return RunExecutionRequest{RunID: cmd.Run.ID, Topology: cmd.Run.Topology, TopologyUID: topologyUID, Revision: cmd.Run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: target, ExpectedSerial: serial}, nil
}
