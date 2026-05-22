package runtime

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// -- sysbox_ssh_access --

type SSHAccessResourceProvider struct{}

func init() {
	RegisterResourceProvider(SSHAccessResourceProvider{})
}

func (SSHAccessResourceProvider) Type() string { return "sysbox_ssh_access" }

func (SSHAccessResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_ssh_access")
}

func (SSHAccessResourceProvider) Read(_ context.Context, current state.Resource) (state.Resource, error) {
	return current, nil
}

func (SSHAccessResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (SSHAccessResourceProvider) Create(ctx context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	return exec.createSSHAccessResource(ctx, n)
}

func (p SSHAccessResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (SSHAccessResourceProvider) Delete(_ context.Context, exec *Executor, current state.Resource) error {
	exec.state.RemoveResource(current.Type, current.Name)
	return nil
}

func (e *Executor) createSSHAccessResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("ssh_access %s: wrong data type", n.ID)
	}

	nodeName := config.ResolveName(cfg.Node)
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return state.Resource{}, fmt.Errorf("node %s not applied yet", nodeName)
	}

	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, err
	}
	handle, err := nodeState.ReconstructHandle(sub)
	if err != nil {
		return state.Resource{}, fmt.Errorf("sysbox_ssh_access: %w", err)
	}

	conn, err := sub.Connection(handle, nil)
	if err != nil || conn == nil {
		return state.Resource{}, fmt.Errorf("sysbox_ssh_access: no connection to node %s: %v", nodeName, err)
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}

	if err := setupSSHAccess(ctx, conn, nodeName, cfg.AuthorizedKeys, port, ""); err != nil {
		return state.Resource{}, fmt.Errorf("setup ssh access on %s: %w", nodeName, err)
	}

	inst := map[string]any{
		"node":         nodeName,
		"port":         port,
		"container_id": handle.ID,
		"key_count":    len(cfg.AuthorizedKeys),
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Type:     "sysbox_ssh_access",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}, nil
}

// setupSSHAccess installs openssh-server, writes authorized_keys and sshd
// config, and starts sshd — all via the substrate.Connection interface so it
// works on any substrate that supports ExecInline / CopyFile.
//
// hookBinaryPath is the host-side path to sysbox-sshd-hook; pass "" to skip.
func setupSSHAccess(ctx context.Context, conn substrate.Connection, nodeID string, authorizedKeys []string, port int, hookBinaryPath string) error {
	// 1. Install openssh-server.
	installCmds := []string{
		"apk add --no-cache openssh-server 2>/dev/null || (apt-get update -qq && apt-get install -y -q openssh-server)",
	}
	if err := conn.ExecInline(ctx, installCmds); err != nil {
		return fmt.Errorf("install openssh: %w", err)
	}

	// 2. Generate host keys.
	if err := conn.ExecInline(ctx, []string{"ssh-keygen -A"}); err != nil {
		return fmt.Errorf("ssh-keygen -A: %w", err)
	}

	// 3. Inject sysbox-sshd-hook binary if provided.
	if hookBinaryPath != "" {
		if err := conn.CopyFile(ctx, hookBinaryPath, "/usr/local/bin/sysbox-sshd-hook"); err != nil {
			return fmt.Errorf("copy sysbox-sshd-hook: %w", err)
		}
		if err := conn.ExecInline(ctx, []string{"chmod +x /usr/local/bin/sysbox-sshd-hook"}); err != nil {
			return fmt.Errorf("chmod hook: %w", err)
		}
	}

	// 4. Write authorized_keys using base64 to avoid heredoc injection.
	// A heredoc with a fixed delimiter like 'SYSBOX_KEYS' can be broken if
	// any key content contains that exact delimiter on its own line,
	// allowing arbitrary shell command execution as root.
	keysContent := strings.Join(authorizedKeys, "\n") + "\n"
	setupKeysCmds := []string{
		"mkdir -p /etc/sysbox && chmod 700 /etc/sysbox",
		fmt.Sprintf("echo %s | base64 -d > /etc/sysbox/authorized_keys",
			base64.StdEncoding.EncodeToString([]byte(keysContent))),
	}
	if err := conn.ExecInline(ctx, setupKeysCmds); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	// 5. Write sshd config using base64 (same injection protection).
	sshdConf := buildSSHDConfig(port, hookBinaryPath != "")
	writeConf := fmt.Sprintf(
		"mkdir -p /etc/ssh/sshd_config.d && echo %s | base64 -d > /etc/ssh/sshd_config.d/sysbox.conf",
		base64.StdEncoding.EncodeToString([]byte(sshdConf)),
	)
	if err := conn.ExecInline(ctx, []string{writeConf}); err != nil {
		return fmt.Errorf("write sshd config: %w", err)
	}

	// 6. Start sshd.
	startCmd := fmt.Sprintf("/usr/sbin/sshd -p %d -f /etc/ssh/sshd_config -D &", port)
	if err := conn.ExecInline(ctx, []string{startCmd}); err != nil {
		return fmt.Errorf("start sshd: %w", err)
	}

	_ = nodeID // available for future structured logging
	return nil
}

func buildSSHDConfig(port int, hasHook bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Port %d\n", port)
	b.WriteString("AuthorizedKeysFile /etc/sysbox/authorized_keys\n")
	b.WriteString("PasswordAuthentication no\n")
	b.WriteString("PermitRootLogin yes\n")
	b.WriteString("StrictModes no\n")

	if hasHook {
		b.WriteString("ForceCommand /usr/local/bin/sysbox-sshd-hook\n")
	}

	envVars := []string{"SYSBOX_NODE_ID", "SYSBOX_REGISTRY_PATH", "SYSBOX_CTRL_SOCK", "SYSBOX_SESSION_ID"}
	b.WriteString("AcceptEnv " + strings.Join(envVars, " ") + "\n")

	return b.String()
}
