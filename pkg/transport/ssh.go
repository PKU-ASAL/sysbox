package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// SSHConnection implements Connection over standard SSH (cli-based).
type SSHConnection struct {
	host         string
	port         string
	user         string
	privateKey   string
	password     string
	insecureHost bool // skip host key verification (for lab environments)
}

func NewSSHConnection(host, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, user: user, privateKey: privateKey, password: password, insecureHost: true}
}

func NewSSHConnectionWithPort(host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, insecureHost: true}
}

// NewSSHConnectionSecure creates an SSH connection that validates host keys.
func NewSSHConnectionSecure(host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password, insecureHost: false}
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
	// Host key verification: disabled for lab environments (default),
	// or strict for production use. Disabling host key verification
	// allows MITM attacks and should only be used in isolated networks.
	if c.insecureHost {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}
	args = append(args, "-o", "LogLevel=ERROR")
	if c.password != "" {
		args = append(args, "-o", "PasswordAuthentication=yes")
	}
	args = append(args, fmt.Sprintf("%s@%s", c.user, c.host))
	return args
}

func (c *SSHConnection) OpenConsole(ctx context.Context, req ConsoleRequest) (substrate.ConsoleSession, error) {
	return NewSSHConsoleSession(ctx, c.sshArgs(), req)
}

func (c *SSHConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (substrate.ExecResult, error) {
	command, err := RemoteCommand(req)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	var stdoutBuffer, stderrBuffer bytes.Buffer
	sshArgs := append(c.sshArgs(), command)
	execCmd := c.command(ctx, sshArgs)
	execCmd.Stdin = req.Stdin
	execCmd.Stdout = &stdoutBuffer
	execCmd.Stderr = &stderrBuffer
	runErr := execCmd.Run()
	result := substrate.ExecResult{Stdout: stdoutBuffer.String(), Stderr: stderrBuffer.String()}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else if runErr != nil {
		return result, fmt.Errorf("ssh exec: %w", runErr)
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, result.Stdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, result.Stderr)
	}
	return result, nil
}

func (c *SSHConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	shellCmd, err := RemoteCommand(req)
	if err != nil {
		return 0, err
	}
	sshArgs := c.sshArgs()
	// Quote the entire shellCmd as a single argument to `sh -c` so that
	// multi-word commands are not split by the remote login shell.
	sshArgs = append(sshArgs, fmt.Sprintf("nohup sh -c %s >/dev/null 2>&1 & echo $!", util.ShellQuote(shellCmd)))

	sshBin := resolveSSHBin()
	ec := exec.CommandContext(ctx, sshBin, sshArgs...)
	out, err := ec.Output()
	if err != nil {
		return 0, fmt.Errorf("ssh background: %w", err)
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid)
	return pid, nil
}

func (c *SSHConnection) command(ctx context.Context, sshArgs []string) *exec.Cmd {
	sshBin := resolveSSHBin()
	if c.password != "" {
		if sp, err := exec.LookPath("sshpass"); err == nil {
			cmd := exec.CommandContext(ctx, sp, append([]string{"-e", sshBin}, sshArgs...)...)
			cmd.Env = append(os.Environ(), "SSHPASS="+c.password)
			return cmd
		}
	}
	return exec.CommandContext(ctx, sshBin, sshArgs...)
}

func (c *SSHConnection) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	scpBin := resolveSCPBin()
	scpArgs := []string{}
	if c.port != "" && c.port != "22" {
		scpArgs = append(scpArgs, "-P", c.port)
	}
	if c.privateKey != "" {
		scpArgs = append(scpArgs, "-i", c.privateKey)
	}
	scpArgs = append(scpArgs, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR")
	scpArgs = append(scpArgs, srcPath, fmt.Sprintf("%s@%s:%s", c.user, c.host, dstPath))

	ec := exec.CommandContext(ctx, scpBin, scpArgs...)
	ec.Stdout = os.Stdout
	ec.Stderr = os.Stderr
	return ec.Run()
}

// WaitForSSH polls the SSH port until the VM's sshd is ready.
func (c *SSHConnection) WaitForSSH(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		result, err := c.Exec(ctx, substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone}, io.Discard, io.Discard)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("ssh not ready after %v", timeout)
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

func resolveSCPBin() string {
	for _, p := range []string{"/usr/bin/scp", "/usr/local/bin/scp"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "scp"
}
