package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

const (
	defaultSSHUser     = "root"
	defaultSSHPass     = "root"
	defaultSSHPort     = 22
	sshConnectTimeout  = 30 * time.Second
)

// ExecInNode runs a command inside the VM via SSH.
func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	cmd := strings.Join(spec.Cmd, " ")
	out, err := sshRun(ctx, h, cmd)
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
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

// ExecBackground starts a detached command inside the VM via SSH.
// Returns the PID of the process inside the VM.
func (s *Substrate) ExecBackground(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (int, error) {
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

// CopyToNode copies a file into the VM via SSH cat.
func (s *Substrate) CopyToNode(ctx context.Context, h substrate.NodeHandle, src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read src %s: %w", src, err)
	}

	ip, port := sshAddrFromHandle(h)
	args := sshArgs(ip, port)
	args = append(args, fmt.Sprintf("cat > '%s'", dst))

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh copy: %w\n%s", err, out)
	}
	return nil
}

func (s *Substrate) CopyFromNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyFromNode: not implemented for firecracker")
}

func (s *Substrate) AttachTTY(_ context.Context, _ substrate.NodeHandle) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("AttachTTY: not implemented for firecracker")
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
	ip, _ := h.Attributes["ssh_ip"].(string)
	port := "22"
	if p, ok := h.Attributes["ssh_port"].(string); ok && p != "" {
		port = p
	}
	return ip, port
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
		out, err = exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return string(out), fmt.Errorf("ssh %s@%s:%s: %w", "root", ip, port, err)
}
