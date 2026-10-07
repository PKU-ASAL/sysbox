package hostsupervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// RunRequest is the only worker-visible input for the first host-run slice.
// It carries immutable revision/action identity, never HCL paths, provider
// handles, credentials, or arbitrary provider arguments.
type RunRequest struct {
	RunID          string `json:"run_id"`
	Topology       string `json:"topology"`
	Revision       string `json:"revision"`
	ActionID       string `json:"action_id"`
	ResourceUID    string `json:"resource_uid"`
	Generation     int64  `json:"generation"`
	Action         string `json:"action"`
	Target         string `json:"target,omitempty"`
	ExpectedSerial int64  `json:"expected_serial"`
}

type RunReceipt struct {
	RunID          string                      `json:"run_id"`
	ActionID       string                      `json:"action_id"`
	Revision       string                      `json:"revision"`
	Scope          controlplane.ExecutionScope `json:"scope"`
	Epoch          uint64                      `json:"epoch"`
	HostBootID     string                      `json:"host_boot_id"`
	Phase          string                      `json:"phase"`
	Status         string                      `json:"status"`
	EvidenceDigest string                      `json:"evidence_digest"`
}

func (r RunRequest) Validate() error {
	if r.RunID == "" || r.Topology == "" || r.Revision == "" || r.ActionID == "" || r.ResourceUID == "" || r.Generation < 1 || r.ExpectedSerial < 0 {
		return fmt.Errorf("run request requires immutable identity")
	}
	if r.Action != "apply" && r.Action != "destroy" {
		return fmt.Errorf("unsupported host run action")
	}
	if strings.ContainsAny(r.RunID+r.Topology+r.Revision+r.ActionID+r.ResourceUID, "/\\\x00") {
		return fmt.Errorf("run request contains unsafe identity")
	}
	return nil
}

func (r RunRequest) Digest() (string, error) {
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

func ValidateRunGrant(g controlplane.SignedExecutionGrant, req RunRequest, boot string) error {
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	if g.Grant.Operation != "run_"+req.Action || g.Grant.Digest != digest || g.Grant.ActionID != req.ActionID || g.Grant.Scope.TopologyUID != req.Topology || g.Grant.Scope.ResourceUID != req.ResourceUID || g.Grant.Scope.Generation != req.Generation || g.Grant.TargetBootID != boot || g.BootID != boot {
		return controlplane.ErrInvalidExecutionGrant
	}
	return nil
}
