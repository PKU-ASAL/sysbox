package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"time"

	vsockexec "github.com/oslab/sysbox/pkg/provider/exec"
	"github.com/oslab/sysbox/pkg/substrate"
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
func vsockConnFromHandle(h substrate.NodeHandle) *vsockexec.VsockConnection {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.VsockUDS == "" {
		return nil
	}
	port := vsockrpc.DefaultPort
	if hs.VsockPort != 0 {
		port = hs.VsockPort
	}
	return vsockexec.NewVsockConnection(hs.VsockUDS, port)
}

// ExecInNode runs a command inside the VM. Prefers the vsock RPC path
// (direct, no SSH dependency) and falls back to SSH for legacy handles
// that lack vsock metadata.
func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	if vc := vsockConnFromHandle(h); vc != nil {
		return s.execInNodeVsock(ctx, vc, spec)
	}
	return s.execInNodeSSH(ctx, h, spec)
}

func (s *Substrate) execInNodeVsock(ctx context.Context, vc *vsockexec.VsockConnection, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	var stdout, stderr strings.Builder
	err := vc.ExecFrameStream(ctx, spec.Cmd, spec.Env, func(f vsockrpc.Frame) error {
		if len(f.Stdout) > 0 {
			stdout.Write(f.Stdout)
		}
		if len(f.Stderr) > 0 {
			stderr.Write(f.Stderr)
		}
		if f.Done {
			return fmt.Errorf("stop") // break the loop
		}
		return nil
	})

	result := substrate.ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err != nil {
		// Check for exit-code errors from ExecStream.
		if strings.HasPrefix(err.Error(), "exit code ") {
			fmt.Sscanf(err.Error(), "exit code %d", &result.ExitCode)
			return result, nil
		}
		// "stop" is our own signal — command completed with Done frame.
		if err.Error() == "stop" {
			return result, nil
		}
		result.ExitCode = 1
		return result, nil
	}
	return result, nil
}

func (s *Substrate) execInNodeSSH(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	cmd := strings.Join(spec.Cmd, " ")
	out, err := sshRun(ctx, h, cmd)
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*osexec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return substrate.ExecResult{
			Stdout:   out,
			ExitCode: exitCode,
		}, nil
	}
	return substrate.ExecResult{
		Stdout:   out,
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
	cmd := strings.Join(spec.Cmd, " ")
	pidCmd := fmt.Sprintf("nohup %s >/dev/null 2>&1 & echo $!", cmd)
	out, err := sshRun(ctx, h, pidCmd)
	if err != nil {
		return 0, fmt.Errorf("exec background: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
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
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read src %s: %w", src, err)
	}

	ip, port := sshAddrFromHandle(h)
	args := sshArgs(ip, port)
	args = append(args, fmt.Sprintf("cat > '%s'", dst))

	cmd := osexec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh copy: %w\n%s", err, out)
	}
	return nil
}

// ObservationHook returns the vsock observation target for the VM.
func (s *Substrate) ObservationHook(_ context.Context, h substrate.NodeHandle) (substrate.ObservationTarget, error) {
	return substrate.ObservationTarget{
		Kind:  "virtio-serial",
		Value: fmt.Sprintf("vm-%s-vsock", h.ID),
	}, nil
}

// ── SSH helpers ─────────────────────────────────────────────────────────────

func sshAddrFromHandle(h substrate.NodeHandle) (string, string) {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil {
		return "", "22"
	}
	port := "22"
	if hs.SSHPort != "" {
		port = hs.SSHPort
	}
	return hs.SSHIP, port
}

func sshArgs(ip, port string) []string {
	user := "root"
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=5",
		"-o", "UserKnownHostsFile=/dev/null",
		"-p", port,
		fmt.Sprintf("%s@%s", user, ip),
	}
}

func sshRun(ctx context.Context, h substrate.NodeHandle, cmd string) (string, error) {
	ip, port := sshAddrFromHandle(h)
	args := sshArgs(ip, port)
	args = append(args, cmd)

	// Retry: VM may still be booting.
	deadline := time.Now().Add(sshConnectTimeout)
	var out []byte
	var err error
	for time.Now().Before(deadline) {
		out, err = osexec.CommandContext(ctx, "ssh", args...).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return string(out), fmt.Errorf("ssh %s@%s:%s: %w", "root", ip, port, err)
}
