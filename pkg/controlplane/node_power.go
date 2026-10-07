package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

// NodePowerRequest describes only the desired power transition. Provider
// handles come from the host's canonical resource repository, never the worker.
type NodePowerRequest struct {
	Topology  string `json:"topology"`
	Node      string `json:"node"`
	Operation string `json:"operation"`
}

var nodePowerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (r NodePowerRequest) Validate() error {
	if !nodePowerName.MatchString(r.Topology) || !nodePowerName.MatchString(r.Node) || (r.Operation != "pause" && r.Operation != "resume") {
		return fmt.Errorf("invalid node power request")
	}
	return nil
}
func (r NodePowerRequest) Digest() (string, error) {
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

func FromNodeOperationCommand(cmd AgentCommand) (NodePowerRequest, error) {
	if cmd.Type != "node_operation" || cmd.ID == "" || cmd.AgentID == "" || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || cmd.ActionID == "" || cmd.Operation.ExternalID != "" {
		return NodePowerRequest{}, fmt.Errorf("node power command requires complete immutable target proof")
	}
	r := NodePowerRequest{Topology: cmd.Operation.Topology, Node: cmd.Operation.Node, Operation: cmd.Operation.Operation}
	return r, r.Validate()
}
