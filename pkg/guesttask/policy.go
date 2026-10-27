package guesttask

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

const PolicyProgram = "sysbox-policy.v1"

// PolicyBuiltin is deliberately distinct from command execution. There is no
// executable path or caller-controlled environment in the privileged branch.
func PolicyBuiltin(r Request) (network.GuestPolicyRequest, error) {
	var p network.GuestPolicyRequest
	if len(r.Args) != 1 || r.Args[0] != PolicyProgram || len(r.Environment) != 0 || r.WorkingDir != "" || len(r.Input) > 1<<20 || r.UserID != 65534 {
		return p, ErrUnauthorized
	}
	d := json.NewDecoder(bytes.NewReader(r.Input))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, ErrConflict
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	scope := r.Grant.Grant.Scope
	if scope.TopologyUID != p.Node.TopologyUID || scope.ResourceUID != p.Node.ResourceUID || scope.Generation != p.Node.Generation {
		return p, ErrUnauthorized
	}
	if p.Operation != "apply" && p.Operation != "delete" {
		return p, ErrUnauthorized
	}
	return p, nil
}

// ExecutePolicyBuiltin is called only by the installed privileged worker mode.
// It independently checks the signed bytes and guest identity before touching nft.
func ExecutePolicyBuiltin(ctx context.Context, r Request, key ed25519.PublicKey, boot string, node substrate.OperationIdentity, root string) (driver.RulesetObservation, error) {
	var empty driver.RulesetObservation
	if err := verifyGrant(r.Grant, key, boot, time.Now()); err != nil {
		return empty, err
	}
	digest, err := r.Digest()
	if err != nil || r.ID != r.Grant.Grant.ActionID || digest != r.Grant.Grant.Digest {
		return empty, ErrUnauthorized
	}
	p, err := PolicyBuiltin(r)
	if err != nil {
		return empty, err
	}
	if node.Validate() != nil || node.OwnerUID != p.Node.OwnerUID || node.TopologyUID != p.Node.TopologyUID || node.ResourceUID != p.Node.ResourceUID || node.Generation != p.Node.Generation {
		return empty, fmt.Errorf("guest policy node binding mismatch")
	}
	budget, err := controlplane.ExecutionGrantBudget(r.Grant, time.Now())
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return (&network.GuestPolicyEngine{Root: root}).Execute(ctx, p)
}
