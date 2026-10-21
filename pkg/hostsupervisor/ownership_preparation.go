package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"net/url"
	"time"
)

func (c AuthorityClient) AuthorizeOwnershipPreparation(ctx context.Context, p controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error) {
	var result controlplane.OwnershipPreparation
	err := c.postJSON(ctx, "/v1/agents/"+url.PathEscape(c.Identity.ID)+"/ownership-transfers/authorize", p, &result)
	return result, err
}
func (c *TaskClient) PrepareOwnershipTransfer(ctx context.Context, p controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error) {
	response, err := c.call(ctx, Request{Operation: "ownership-prepare", OwnershipPlan: &p})
	if err != nil {
		return controlplane.OwnershipPreparation{}, err
	}
	if response.OwnershipPreparation == nil {
		return controlplane.OwnershipPreparation{}, fmt.Errorf("missing preparation proof")
	}
	return *response.OwnershipPreparation, nil
}
func PrepareOwnership(ctx context.Context, p controlplane.OwnershipTransferPlan, agent, boot string, key ed25519.PrivateKey, authorize func(context.Context, controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error), inspect func(context.Context, controlplane.OwnershipTransferPlan) error) (controlplane.OwnershipPreparation, error) {
	var zero controlplane.OwnershipPreparation
	if p.AgentID != agent || authorize == nil || inspect == nil || p.Validate() != nil {
		return zero, fmt.Errorf("invalid transfer preparation")
	}
	first, err := authorize(ctx, p)
	if err != nil {
		return zero, err
	}
	if err = inspect(ctx, p); err != nil {
		return zero, err
	}
	second, err := authorize(ctx, p)
	if err != nil {
		return zero, err
	}
	a, _ := p.Digest()
	b, _ := second.Plan.Digest()
	if a != b || first.SourceEpoch != second.SourceEpoch {
		return zero, fmt.Errorf("transfer freeze changed")
	}
	second.HostBootID = boot
	second.ObservedAt = time.Now().UTC()
	return controlplane.SignOwnershipPreparation(key, second)
}
