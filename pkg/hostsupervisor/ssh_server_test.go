package hostsupervisor

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSSHAccessSocketRoutesOnlyThroughTrustedCallbacks(t *testing.T) {
	c, req, g := sshFixture(t)
	require.NoError(t, os.Chmod(c.Root, 0700))
	socketDir := t.TempDir()
	require.NoError(t, os.Chmod(socketDir, 0700))
	calls := 0
	s := &Server{Socket: filepath.Join(socketDir, "ssh.sock"), GroupID: -1, SSH: c, SSHApply: func(context.Context, controlplane.SSHAccessRequest, controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		calls++
		return nil, context.DeadlineExceeded
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	client := NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: req.Node, Scope: g.Grant.Scope})
	require.Eventually(t, func() bool {
		c, e := net.DialTimeout("unix", s.Socket, 10*time.Millisecond)
		if e == nil {
			c.Close()
			return true
		}
		return false
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { _, err := client.ApplySSHAccess(ctx, req, g); return err != nil }, time.Second, 10*time.Millisecond)
	require.Equal(t, 1, calls)
	_, err := client.ObserveSSHAccess(ctx, req, g)
	require.Error(t, err)
	cancel()
	require.NoError(t, <-done)
}
