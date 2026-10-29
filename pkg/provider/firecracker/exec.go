package firecracker

import (
	"context"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

const (
	defaultSSHUser = "root"
	defaultSSHPass = "root"
)

// ExecInNode is the transitional GuestExec adapter. Node.Connection owns all
// transport selection, identity, namespace and trust settings.
func (s *Substrate) ExecInNode(ctx context.Context, handle substrate.NodeHandle, req substrate.ExecRequest) (substrate.ExecResult, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	return conn.Exec(ctx, req, nil, nil)
}

func (s *Substrate) CopyToNode(ctx context.Context, handle substrate.NodeHandle, src, dst string, mode uint32) error {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	return transport.InstallFile(ctx, conn, src, dst, mode)
}
