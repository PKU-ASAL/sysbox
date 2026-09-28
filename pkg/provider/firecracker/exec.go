package firecracker

import (
	"context"
	"fmt"

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

func (s *Substrate) ExecBackground(ctx context.Context, handle substrate.NodeHandle, req substrate.ExecRequest) (int, error) {
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return 0, err
	}
	starter, ok := conn.(substrate.ConnectionStarter)
	if !ok {
		return 0, substrate.ErrNotSupported
	}
	execution, err := starter.Start(ctx, req)
	return execution.PID, err
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
	files, ok := conn.(substrate.ConnectionFileCopier)
	if !ok {
		return substrate.ErrNotSupported
	}
	tmp := dst + ".sysbox-tmp"
	if err := files.CopyFile(ctx, src, tmp); err != nil {
		return err
	}
	result, err := conn.Exec(ctx, guestFileInstallRequest("chmod", fmt.Sprintf("%04o", mode), tmp), nil, nil)
	if err != nil {
		return fmt.Errorf("set guest file mode: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("set guest file mode: exit code %d", result.ExitCode)
	}
	result, err = conn.Exec(ctx, guestFileInstallRequest("mv", "-f", tmp, dst), nil, nil)
	if err != nil {
		return fmt.Errorf("atomic guest file rename: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("atomic guest file rename: exit code %d", result.ExitCode)
	}
	return nil
}

func guestFileInstallRequest(program string, args ...string) substrate.ExecRequest {
	return substrate.ExecRequest{Program: program, Args: args, Shell: substrate.ShellNone}
}
