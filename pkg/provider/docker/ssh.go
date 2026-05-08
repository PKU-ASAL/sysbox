package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

// SSHAccessSpec describes how to configure SSH access on a node.
type SSHAccessSpec struct {
	NodeHandle      substrate.NodeHandle
	NodeID          string
	AuthorizedKeys  []string
	Port            int
	RegistryPath    string // path to session-registry.json (mounted into container)
	CtrlSockPath    string // path to the sensor control socket (mounted into container)
	HookBinaryPath  string // local path to sysbox-sshd-hook binary to inject
}

// SetupSSHAccess installs openssh-server, injects the sysbox-sshd-hook binary,
// writes authorized_keys and sshd config, and starts sshd.
//
// The container must be running (StartNode already called).
func (s *Substrate) SetupSSHAccess(ctx context.Context, spec SSHAccessSpec) error {
	h := spec.NodeHandle
	port := spec.Port
	if port == 0 {
		port = 22
	}

	// 1. Install openssh-server (Alpine uses apk).
	if res, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
		Cmd: []string{"apk", "add", "--no-cache", "openssh-server"},
	}); err != nil || res.ExitCode != 0 {
		// Try apt-get if apk fails (Debian/Ubuntu containers).
		if res2, err2 := s.ExecInNode(ctx, h, substrate.ExecSpec{
			Cmd: []string{"sh", "-c", "apt-get update -qq && apt-get install -y -q openssh-server 2>&1"},
		}); err2 != nil || res2.ExitCode != 0 {
			return fmt.Errorf("install openssh: %v / %v", err, err2)
		}
	}

	// 2. Generate host keys.
	if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
		Cmd: []string{"ssh-keygen", "-A"},
	}); err != nil {
		return fmt.Errorf("ssh-keygen -A: %w", err)
	}

	// 3. Inject sysbox-sshd-hook binary if provided.
	if spec.HookBinaryPath != "" {
		if err := s.CopyToNode(ctx, h, spec.HookBinaryPath, "/usr/local/bin/"); err != nil {
			return fmt.Errorf("copy sysbox-sshd-hook: %w", err)
		}
		if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
			Cmd: []string{"chmod", "+x", "/usr/local/bin/sysbox-sshd-hook"},
		}); err != nil {
			return fmt.Errorf("chmod hook: %w", err)
		}
	}

	// 4. Write authorized_keys.
	keysContent := strings.Join(spec.AuthorizedKeys, "\n") + "\n"
	mkdirCmd := []string{"sh", "-c", "mkdir -p /etc/sysbox && chmod 700 /etc/sysbox"}
	if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{Cmd: mkdirCmd}); err != nil {
		return fmt.Errorf("mkdir /etc/sysbox: %w", err)
	}
	writeKeys := fmt.Sprintf("cat > /etc/sysbox/authorized_keys << 'KEYS'\n%sKEYS", keysContent)
	if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
		Cmd: []string{"sh", "-c", writeKeys},
	}); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	// 5. Write sshd config.
	sshdConf := buildSSHDConfig(spec, port)
	writeConf := fmt.Sprintf("mkdir -p /etc/ssh/sshd_config.d && cat > /etc/ssh/sshd_config.d/sysbox.conf << 'EOF'\n%sEOF", sshdConf)
	if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
		Cmd: []string{"sh", "-c", writeConf},
	}); err != nil {
		return fmt.Errorf("write sshd config: %w", err)
	}

	// 6. Start sshd.
	if _, err := s.ExecInNode(ctx, h, substrate.ExecSpec{
		Cmd: []string{"sh", "-c", fmt.Sprintf("/usr/sbin/sshd -p %d -f /etc/ssh/sshd_config -D &", port)},
	}); err != nil {
		return fmt.Errorf("start sshd: %w", err)
	}

	return nil
}

func buildSSHDConfig(spec SSHAccessSpec, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Port %d\n", port)
	b.WriteString("AuthorizedKeysFile /etc/sysbox/authorized_keys\n")
	b.WriteString("PasswordAuthentication no\n")
	b.WriteString("PermitRootLogin yes\n")
	b.WriteString("StrictModes no\n")

	if spec.HookBinaryPath != "" {
		b.WriteString("ForceCommand /usr/local/bin/sysbox-sshd-hook\n")
	}

	// Environment variables forwarded into the hook.
	envVars := []string{"SYSBOX_NODE_ID", "SYSBOX_REGISTRY_PATH", "SYSBOX_CTRL_SOCK", "SYSBOX_SESSION_ID"}
	b.WriteString("AcceptEnv " + strings.Join(envVars, " ") + "\n")

	return b.String()
}
