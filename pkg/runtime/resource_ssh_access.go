package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// -- sysbox_ssh_access --

func (e *Executor) createSSHAccess(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.SSHAccessConfig)
	if !ok {
		return fmt.Errorf("ssh_access %s: wrong data type", n.ID)
	}

	nodeName := config.ResolveName(cfg.Node)
	nodeState := e.state.FindResource("sysbox_node", nodeName)
	if nodeState == nil {
		return fmt.Errorf("node %s not applied yet", nodeName)
	}

	containerID := util.AsString(nodeState.Instance["container_id"])
	handle := substrate.NodeHandle{
		ID: containerID,
		Provider: &dockerprovider.HandleState{
			ContainerName: fmt.Sprintf("sysbox-%s", nodeName),
		},
		Conn: substrate.ConnInfo{
			Kind:     substrate.ConnKindDocker,
			Endpoint: containerID,
		},
	}

	subName := nodeState.Provider
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	conn, err := sub.Connection(handle, nil)
	if err != nil || conn == nil {
		return fmt.Errorf("sysbox_ssh_access: no connection to node %s: %v", nodeName, err)
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}

	if err := setupSSHAccess(ctx, conn, nodeName, cfg.AuthorizedKeys, port, ""); err != nil {
		return fmt.Errorf("setup ssh access on %s: %w", nodeName, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_ssh_access",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"node": nodeName,
			"port": port,
		},
	})
	return nil
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

	// 4. Write authorized_keys.
	keysContent := strings.Join(authorizedKeys, "\n") + "\n"
	setupKeysCmds := []string{
		"mkdir -p /etc/sysbox && chmod 700 /etc/sysbox",
		fmt.Sprintf("cat > /etc/sysbox/authorized_keys << 'SYSBOX_KEYS'\n%sSYSBOX_KEYS", keysContent),
	}
	if err := conn.ExecInline(ctx, setupKeysCmds); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	// 5. Write sshd config.
	sshdConf := buildSSHDConfig(port, hookBinaryPath != "")
	writeConf := fmt.Sprintf(
		"mkdir -p /etc/ssh/sshd_config.d && cat > /etc/ssh/sshd_config.d/sysbox.conf << 'SYSBOX_SSHD'\n%sSYSBOX_SSHD",
		sshdConf,
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
