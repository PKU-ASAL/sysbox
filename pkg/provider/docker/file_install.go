package docker

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"io"
)

func (c *dockerConn) GuestFileBootID(ctx context.Context) (string, error) {
	return c.tasks().GuestFileBootID(ctx)
}
func (c *dockerConn) InstallFile(ctx context.Context, r substrate.FileInstallRequest, data io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	return c.tasks().InstallFile(ctx, r, data)
}
func (c *dockerConn) ObserveFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return c.tasks().ObserveFileInstall(ctx, h)
}
func (c *dockerConn) CancelFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return c.tasks().CancelFileInstall(ctx, h)
}
