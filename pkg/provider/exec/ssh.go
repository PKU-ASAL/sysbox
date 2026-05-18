package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SSHConnection implements Connection over standard SSH (cli-based).
type SSHConnection struct {
	host       string
	port       string
	user       string
	privateKey string
	password   string
}

func NewSSHConnection(host, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, user: user, privateKey: privateKey, password: password}
}

func NewSSHConnectionWithPort(host, port, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, port: port, user: user, privateKey: privateKey, password: password}
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
	// Strict host key checking off for lab environments.
	args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR")
	if c.password != "" {
		args = append(args, "-o", "PasswordAuthentication=yes")
	}
	args = append(args, fmt.Sprintf("%s@%s", c.user, c.host))
	return args
}

func (c *SSHConnection) ExecInline(ctx context.Context, cmds []string) error {
	for _, cmd := range cmds {
		if err := c.execOne(ctx, cmd, nil); err != nil {
			return fmt.Errorf("ssh exec %q: %w", cmd, err)
		}
	}
	return nil
}

// ExecCapture runs a command over SSH and returns its stdout.
func (c *SSHConnection) ExecStream(ctx context.Context, cmds []string, stdout, stderr io.Writer) error {
	for _, cmd := range cmds {
		var buf bytes.Buffer
		if err := c.execOne(ctx, cmd, &buf); err != nil {
			return fmt.Errorf("ssh exec %q: %w", cmd, err)
		}
		stdout.Write(buf.Bytes()) //nolint:errcheck
	}
	return nil
}

func (c *SSHConnection) ExecCapture(ctx context.Context, cmd string) ([]byte, error) {
	var stdout bytes.Buffer
	if err := c.execOne(ctx, cmd, &stdout); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

func (c *SSHConnection) execOne(ctx context.Context, cmd string, stdoutWriter *bytes.Buffer) error {
	sshArgs := c.sshArgs()
	sshArgs = append(sshArgs, cmd)

	var sshBin string
	for _, p := range []string{"/usr/bin/ssh", "/usr/local/bin/ssh", "ssh"} {
		if _, err := os.Stat(p); err == nil {
			sshBin = p
			break
		}
	}
	if sshBin == "" {
		sshBin = "ssh"
	}

	// Use sshpass for password auth if available.
	var execCmd *exec.Cmd
	if c.password != "" {
		if sp, err := exec.LookPath("sshpass"); err == nil {
			spArgs := []string{"-p", c.password, sshBin}
			spArgs = append(spArgs, sshArgs...)
			execCmd = exec.CommandContext(ctx, sp, spArgs...)
		}
	}
	if execCmd == nil {
		execCmd = exec.CommandContext(ctx, sshBin, sshArgs...)
	}

	var stderr bytes.Buffer
	execCmd.Stderr = &stderr
	if stdoutWriter != nil {
		execCmd.Stdout = stdoutWriter
	} else {
		execCmd.Stdout = os.Stdout
	}

	if err := execCmd.Run(); err != nil {
		return fmt.Errorf("%s\n%s", err, stderr.String())
	}
	return nil
}

func (c *SSHConnection) ExecBackground(ctx context.Context, cmd []string, env map[string]string) (int, error) {
	// Build env prefix.
	envPrefix := ""
	for k, v := range env {
		envPrefix += fmt.Sprintf("export %s=%q; ", k, v)
	}
	shellCmd := envPrefix + strings.Join(cmd, " ")

	sshArgs := c.sshArgs()
	sshArgs = append(sshArgs, "nohup sh -c "+shellCmd+" & echo $!")

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
		if err := c.execOne(ctx, "true", nil); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("ssh not ready after %v", timeout)
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
