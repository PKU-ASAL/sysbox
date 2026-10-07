package transport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// SSHConnection implements Connection over standard SSH (cli-based).
type SSHConnection struct {
	executionIdentity
	host         string
	port         string
	user         string
	privateKey   string
	password     string
	insecureHost bool // skip host key verification (for lab environments)
	knownHosts   string
	netns        string
	runCommand   func(*exec.Cmd) error
}

func NewSSHConnection(host, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, user: user, privateKey: privateKey, password: password}
}

func NewSSHConnectionWithPort(host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password}
}

func NewSSHConnectionInNamespace(namespace, host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, netns: namespace}
}

func NewSSHConnectionInNamespaceWithTrust(namespace, host, port, user, privateKey, password, knownHosts string, insecure bool) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, knownHosts: knownHosts, insecureHost: insecure, netns: namespace}
}

// NewSSHConnectionSecure creates an SSH connection that validates host keys.
func NewSSHConnectionSecure(host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, insecureHost: false}
}

func NewSSHConnectionWithTrust(host, port, user, privateKey, password, knownHosts string, insecure bool) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, knownHosts: knownHosts, insecureHost: insecure}
}

// Host returns the SSH target host.
func (c *SSHConnection) Host() string {
	return c.host
}

// sshArgs builds the ssh command arguments.
func (c *SSHConnection) sshArgs() []string {
	args := []string{}
	if c.port != "" && c.port != "22" {
		args = append(args, "-p", c.port)
	}
	if c.privateKey != "" {
		args = append(args, "-i", c.privateKey)
	}
	// Strict by default; lab-only host key bypass must be explicit.
	args = append(args, c.hostKeyArgs()...)
	args = append(args, "-o", "LogLevel=ERROR")
	if c.password != "" {
		args = append(args, "-o", "PasswordAuthentication=yes")
	}
	args = append(args, fmt.Sprintf("%s@%s", c.user, c.host))
	return args
}

func (c *SSHConnection) hostKeyArgs() []string {
	if c.insecureHost {
		return []string{"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	}
	args := []string{"-o", "StrictHostKeyChecking=yes"}
	if c.knownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+c.knownHosts)
	}
	return args
}

func (c *SSHConnection) OpenConsole(ctx context.Context, req ConsoleRequest) (substrate.ConsoleSession, error) {
	if c.netns == "" {
		return NewSSHConsoleSession(ctx, c.sshArgs(), req)
	}
	return NewSSHConsoleSessionWithRunner(ctx, "ip", append([]string{"netns", "exec", c.netns, resolveSSHBin()}, c.sshArgs()...), req)
}

func (c *SSHConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	defer func() { result, err = substrate.FinishExecution(ctx, result, err) }()
	command, err := RemoteCommand(req)
	if err != nil {
		return substrate.ExecResult{ErrorClass: substrate.ExecErrorInvalid}, err
	}
	stdoutBuffer := substrate.NewExecCapture(req.OutputLimitBytes, stdout)
	stderrBuffer := substrate.NewExecCapture(req.OutputLimitBytes, stderr)
	sshArgs := append(c.sshArgs(), command)
	execCmd := c.command(ctx, sshArgs)
	execCmd.Stdin = req.Stdin
	execCmd.WaitDelay = time.Second
	execCmd.Stdout = stdoutBuffer
	execCmd.Stderr = stderrBuffer
	run := c.runCommand
	if run == nil {
		run = func(cmd *exec.Cmd) error { return cmd.Run() }
	}
	runErr := run(execCmd)
	result = substrate.ExecResult{Stdout: stdoutBuffer.String(), Stderr: stderrBuffer.String(), StdoutTruncated: stdoutBuffer.Truncated(), StderrTruncated: stderrBuffer.Truncated()}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		if result.ExitCode < 0 || result.ExitCode == 255 {
			return result, fmt.Errorf("ssh exec: %w", runErr)
		}
	} else if runErr != nil {
		return result, fmt.Errorf("ssh exec: %w", runErr)
	}
	result.ExitCodeKnown = true
	return result, nil
}

func (c *SSHConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	handle, err := c.Start(ctx, req)
	return handle.PID, err
}

func (c *SSHConnection) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	return startPOSIX(ctx, c, c.executionIdentity, c.host, req)
}

func (c *SSHConnection) command(ctx context.Context, sshArgs []string) *exec.Cmd {
	sshBin := resolveSSHBin()
	if c.password != "" {
		if sp, err := exec.LookPath("sshpass"); err == nil {
			args := append([]string{"-e", sshBin}, sshArgs...)
			cmd := c.namespacedCommand(ctx, sp, args...)
			cmd.Env = append(os.Environ(), "SSHPASS="+c.password)
			return cmd
		}
	}
	return c.namespacedCommand(ctx, sshBin, sshArgs...)
}

func (c *SSHConnection) namespacedCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	if c.netns == "" {
		return exec.CommandContext(ctx, binary, args...)
	}
	return exec.CommandContext(ctx, "ip", append([]string{"netns", "exec", c.netns, binary}, args...)...)
}

func (c *SSHConnection) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source must be a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	expected := fmt.Sprintf("%x", digest.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Use the same identity/trust/password/namespace path as Exec. The original
	// destination survives incomplete upload or a failed checksum.
	script := "set -eu; tmp=$(mktemp -- " + util.ShellQuote(dstPath+".sysbox-transfer.XXXXXX") + "); " +
		"trap 'rm -f -- \"$tmp\"' EXIT; cat > \"$tmp\"; " +
		"actual=$(sha256sum < \"$tmp\"); actual=${actual%% *}; " +
		"[ \"$actual\" = " + util.ShellQuote(expected) + " ]; " +
		fmt.Sprintf("chmod %04o -- \"$tmp\"; sync -f \"$tmp\"; ", info.Mode().Perm()) +
		"mv -fT -- \"$tmp\" " + util.ShellQuote(dstPath) + "; sync -f " + util.ShellQuote(filepath.Dir(dstPath)) + "; printf '%s' \"$actual\""
	result, err := c.Exec(ctx, substrate.ExecRequest{Program: script, Shell: substrate.ShellLinux, Stdin: file, OutputLimitBytes: 128}, nil, nil)
	if err != nil {
		return err
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 || result.StdoutTruncated || result.Stdout != expected {
		return fmt.Errorf("SSH file install lacks verified successful checksum receipt")
	}
	return nil
}

// WaitForSSH polls the SSH port until the VM's sshd is ready.
func (c *SSHConnection) WaitForSSH(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		result, err := c.Exec(ctx, substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone}, io.Discard, io.Discard)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// WaitReady implements substrate.ConnectionWaiter.
func (c *SSHConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	return c.WaitForSSH(ctx, timeout)
}

func resolveSSHBin() string {
	for _, p := range []string{"/usr/bin/ssh", "/usr/local/bin/ssh"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "ssh"
}
