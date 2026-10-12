package hostsupervisor

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// canonicalGuestUser reads the node's persisted desired configuration, never
// a worker payload or a current, potentially unpublished workspace. Decode only
// the account fields; connection credentials must not escape into discovery.
func canonicalGuestUser(resource state.Resource) (string, error) {
	return substrate.CanonicalGuestUser(resource.Attributes["desired"])
}

func canonicalConnection(ctx context.Context, target NodeTarget) (Target, error) {
	if target.canonicalResource == nil {
		return Target{}, fmt.Errorf("canonical guest configuration unavailable")
	}
	user, err := canonicalGuestUser(*target.canonicalResource)
	if err != nil {
		return Target{}, err
	}
	hints, err := substrate.CanonicalConnectionHints(ctx, target.canonicalResource.Attributes["desired"])
	if err != nil {
		return Target{}, err
	}
	conn, err := target.Node.Connection(target.Handle, hints)
	if err != nil {
		return Target{}, err
	}
	return Target{Scope: target.Scope, AgentID: target.AgentID, NodeID: target.Handle.ID, User: user, Connection: conn}, nil
}
