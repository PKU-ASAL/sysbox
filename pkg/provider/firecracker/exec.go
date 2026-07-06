package firecracker

import (
	"context"
	"fmt"
	"io"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

const (
	defaultSSHUser    = "root"
	defaultSSHPass    = "root"
	defaultSSHPort    = 22
	sshConnectTimeout = 30 * time.Second
	vsockReadyTimeout = 30 * time.Second
)

// vsockConnFromHandle builds a VsockConnection from the handle's typed
// HandleState. Returns nil if vsock metadata is missing (rootfs without
// sysbox-init).
func vsockConnFromHandle(h substrate.NodeHandle) *transport.VsockConnection {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.VsockUDS == "" {
		return nil
	}
	port := vsockrpc.DefaultPort
	if hs.VsockPort != 0 {
		port = hs.VsockPort
	}
	return transport.NewVsockConnection(hs.VsockUDS, port)
}

// ExecInNode runs a command inside the VM. It uses vsock RPC when the handle
// contains vsock metadata, otherwise it uses SSH.
func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	if vc := vsockConnFromHandle(h); vc != nil {
		return s.execInNodeVsock(ctx, vc, spec)
	}
	return s.execInNodeSSH(ctx, h, spec)
}

func (s *Substrate) OpenConsole(ctx context.Context, h substrate.NodeHandle, req substrate.ConsoleRequest) (substrate.ConsoleSession, error) {
	if vc := vsockConnFromHandle(h); vc != nil {
		return vc.OpenConsole(ctx, transport.ConsoleRequest{
			Cmd:   req.Cmd,
			Shell: req.Shell,
			Env:   req.Env,
			Cols:  req.Cols,
			Rows:  req.Rows,
		})
	}
	ssh := sshConnFromHandle(h)
	if ssh == nil {
		return nil, fmt.Errorf("no vsock or SSH console info in handle")
	}
	return ssh.OpenConsole(ctx, transport.ConsoleRequest{
		Cmd:   req.Cmd,
		Shell: req.Shell,
		Env:   req.Env,
		Cols:  req.Cols,
		Rows:  req.Rows,
	})
}

var _ substrate.ConsoleProvider = (*Substrate)(nil)

func (s *Substrate) execInNodeVsock(ctx context.Context, vc *transport.VsockConnection, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	var stdout, stderr strings.Builder
	var exitCode int
	err := vc.ExecFrameStream(ctx, spec.Cmd, spec.Env, func(f vsockrpc.Frame) error {
		if len(f.Stdout) > 0 {
			stdout.Write(f.Stdout)
		}
		if len(f.Stderr) > 0 {
			stderr.Write(f.Stderr)
		}
		if f.Done {
			exitCode = f.ExitCode
			return io.EOF // signal clean stop; ExecFrameStream returns nil
		}
		return nil
	})

	result := substrate.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
	if err != nil {
		// Check for exit-code errors from ExecStream (non-frame-stream callers).
		if strings.HasPrefix(err.Error(), "exit code ") {
			fmt.Sscanf(err.Error(), "exit code %d", &result.ExitCode)
			return result, nil
		}
		// io.EOF means we signalled stop via Done frame — not an error.
		if err == io.EOF {
			return result, nil
		}
		result.ExitCode = 1
		return result, nil
	}
	return result, nil
}

func (s *Substrate) execInNodeSSH(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	conn := sshConnFromHandle(h)
	if conn == nil {
		return substrate.ExecResult{}, fmt.Errorf("no SSH connection info in handle")
	}
	cmd := strings.Join(spec.Cmd, " ")
	out, err := conn.ExecCapture(ctx, cmd)
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*osexec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return substrate.ExecResult{
			Stdout:   string(out),
			ExitCode: exitCode,
		}, nil
	}
	return substrate.ExecResult{
		Stdout:   string(out),
		ExitCode: 0,
	}, nil
}

// ExecBackground starts a detached command inside the VM. Prefers vsock
// and falls back to SSH.
func (s *Substrate) ExecBackground(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (int, error) {
	if vc := vsockConnFromHandle(h); vc != nil {
		return vc.ExecBackground(ctx, spec.Cmd, spec.Env)
	}
	return s.execBackgroundSSH(ctx, h, spec)
}

func (s *Substrate) execBackgroundSSH(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (int, error) {
	conn := sshConnFromHandle(h)
	if conn == nil {
		return 0, fmt.Errorf("no SSH connection info in handle")
	}
	pid, err := conn.ExecBackground(ctx, spec.Cmd, spec.Env)
	if err != nil {
		return 0, fmt.Errorf("ssh background: %w", err)
	}
	return pid, nil
}

// CopyToNode copies a file into the VM. Prefers vsock write_file,
// falls back to SSH cat.
func (s *Substrate) CopyToNode(ctx context.Context, h substrate.NodeHandle, src, dst string) error {
	if vc := vsockConnFromHandle(h); vc != nil {
		return vc.CopyFile(ctx, src, dst)
	}
	return s.copyToNodeSSH(ctx, h, src, dst)
}

func (s *Substrate) copyToNodeSSH(ctx context.Context, h substrate.NodeHandle, src, dst string) error {
	conn := sshConnFromHandle(h)
	if conn == nil {
		return fmt.Errorf("no SSH connection info in handle")
	}
	return conn.CopyFile(ctx, src, dst)
}

// sshConnFromHandle constructs an SSHConnection from the firecracker handle's
// SSH metadata. Returns nil if no SSH info is available.
func sshConnFromHandle(h substrate.NodeHandle) *transport.SSHConnection {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.SSHIP == "" {
		return nil
	}
	port := "22"
	if hs.SSHPort != "" {
		port = hs.SSHPort
	}
	return transport.NewSSHConnectionWithPort(hs.SSHIP, port, "root", "", "")
}
