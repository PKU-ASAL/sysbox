package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/provider/network"
)

// ObserveGuestPolicy sends only a read-only typed query. All mutations travel
// through Start and the signed durable task protocol.
func (c *VsockConnection) ObserveGuestPolicy(ctx context.Context, p network.GuestPolicyRequest) (driver.RulesetObservation, error) {
	var empty driver.RulesetObservation
	if p.Operation != "observe" && p.Operation != "discover" {
		return empty, guesttask.ErrUnauthorized
	}
	response, err := c.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "policy-observe", PolicyQuery: &p})
	if err != nil {
		return empty, err
	}
	if response.PolicyAbsent {
		return empty, driver.Wrap(driver.ErrorNotFound, "firecracker", "guest policy absent", nil)
	}
	if response.Policy == nil {
		return empty, guesttask.ErrUnknown
	}
	return *response.Policy, nil
}
