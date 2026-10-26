package hostsupervisor

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"regexp"
	"strings"
)

// RunRecoveryRequest carries delivery proof only. The host independently asks
// the API for the canonical Run, mode, revision and current authorization.
type RunRecoveryRequest struct {
	CommandID  string `json:"command_id"`
	LeaseOwner string `json:"lease_owner"`
	Attempt    int    `json:"attempt"`
}

var recoveryCommandID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (r RunRecoveryRequest) Validate() error {
	if !recoveryCommandID.MatchString(r.CommandID) || strings.TrimSpace(r.LeaseOwner) == "" || r.Attempt < 1 {
		return fmt.Errorf("valid recovery command lease required")
	}
	return nil
}

func (c AuthorityClient) AuthorizeRunRecovery(ctx context.Context, proof RunRecoveryRequest) (controlplane.RunRecoveryAuthorization, error) {
	var out controlplane.RunRecoveryAuthorization
	if err := proof.Validate(); err != nil {
		return out, err
	}
	// Revision uploads permit 8 MiB JSON. Re-encoding the complete file tree
	// as base64 plus the Run envelope needs a larger, still bounded response.
	err := c.postJSONLimit(ctx, "/v1/agents/"+c.Identity.ID+"/commands/"+proof.CommandID+"/recovery-authorize", struct {
		RunRecoveryRequest
		InputsProtocol int `json:"inputs_protocol"`
	}{proof, 1}, &out, 16<<20)
	return out, err
}

func (c *TaskClient) RecoverRun(ctx context.Context, proof RunRecoveryRequest) (*controlplane.RunRecoveryReport, error) {
	if err := proof.Validate(); err != nil {
		return nil, err
	}
	response, err := c.call(ctx, Request{Operation: "run-recovery", RecoveryRequest: &proof})
	if err != nil {
		return nil, err
	}
	if response.RecoveryReport == nil {
		return nil, fmt.Errorf("host recovery report missing")
	}
	return response.RecoveryReport, nil
}
