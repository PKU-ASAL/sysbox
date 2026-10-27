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

// Console has its own session/TTY contract but uses the same connection identity.
func (s *Substrate) OpenConsole(ctx context.Context, handle substrate.NodeHandle, req substrate.ConsoleRequest) (substrate.ConsoleSession, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return nil, err
	}
	console := transport.ConsoleRequest{Cmd: req.Cmd, Shell: req.Shell, Env: req.Env, Cols: req.Cols, Rows: req.Rows}
	switch conn := conn.(type) {
	case *transport.SSHConnection:
		return conn.OpenConsole(ctx, console)
	case *transport.VsockConnection:
		return conn.OpenConsole(ctx, console)
	default:
		return nil, substrate.ErrNotSupported
	}
}

var _ substrate.ConsoleProvider = (*Substrate)(nil)

func (s *Substrate) CopyToNode(ctx context.Context, handle substrate.NodeHandle, src, dst string, mode uint32) error {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	return transport.InstallFile(ctx, conn, src, dst, mode)
}
