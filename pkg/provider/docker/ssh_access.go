package docker

import (
	"context"

	"github.com/oslab/sysbox/pkg/substrate"
)

// All helper calls pass through dockerConn.Exec, which validates the immutable
// provider identity and generation before creating a Docker exec instance.
func (c *dockerConn) DiscoverSSHAccess(ctx context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return c.tasks().DiscoverSSHAccess(ctx, q)
}

func (c *dockerConn) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	return c.tasks().MutateSSHAccess(ctx, r)
}

func (c *dockerConn) ObserveSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	return c.tasks().ObserveSSHAccess(ctx, h)
}

func (c *dockerConn) InspectSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return c.tasks().InspectSSHAccess(ctx, h)
}

var _ substrate.ConnectionSSHAccess = (*dockerConn)(nil)
