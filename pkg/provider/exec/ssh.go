package exec

import (
	"context"
	"fmt"
)

// SSHConnection implements Connection over standard SSH.
// This is a stub for Phase 3 (VM / bare-metal substrates).
// It will be wired up when the Firecracker or QEMU substrate is added.
type SSHConnection struct {
	host       string
	user       string
	privateKey string
	password   string
}

func NewSSHConnection(host, user, privateKey, password string) *SSHConnection {
	return &SSHConnection{host: host, user: user, privateKey: privateKey, password: password}
}

func (c *SSHConnection) ExecInline(_ context.Context, _ []string) error {
	return fmt.Errorf("SSHConnection: not implemented (Phase 3)")
}

func (c *SSHConnection) ExecBackground(_ context.Context, _ []string, _ map[string]string) (int, error) {
	return 0, fmt.Errorf("SSHConnection: not implemented (Phase 3)")
}

func (c *SSHConnection) CopyFile(_ context.Context, _, _ string) error {
	return fmt.Errorf("SSHConnection: not implemented (Phase 3)")
}
