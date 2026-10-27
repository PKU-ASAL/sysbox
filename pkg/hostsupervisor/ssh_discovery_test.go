package hostsupervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type sshDiscoveryConnection struct {
	taskConnection
	query substrate.SSHAccessQuery
	calls int
	wrong bool
}

func TestHostSSHDiscoverySocketRoundTrip(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 2}}
	conn := &sshDiscoveryConnection{}
	d := &Dispatcher{Root: filepath.Join(root, "journal"), AgentID: "agent", Resolve: func(_ context.Context, asked TargetRef) (Target, error) {
		if asked != ref {
			return Target{}, substrate.ErrNotSupported
		}
		return Target{Scope: ref.Scope, AgentID: "agent", NodeID: "provider-id", User: "ubuntu", Connection: conn}, nil
	}}
	server := &Server{Socket: filepath.Join(root, "host.sock"), GroupID: -1, Dispatcher: d}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("unix", server.Socket, 10*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, time.Second, 10*time.Millisecond)
	client := NewTaskClient(server.Socket, ref)
	found, err := client.DiscoverManagedSSHAccess(ctx, "admin")
	require.NoError(t, err)
	require.Equal(t, "web", found.Query.NodeID)
	require.Equal(t, "ubuntu", found.Query.User)
	require.Equal(t, "guest", found.BootID)
	require.Equal(t, "absent", found.Status)
	_, err = client.DiscoverManagedSSHAccess(ctx, "../invalid")
	require.Error(t, err)
}

func (c *sshDiscoveryConnection) DiscoverSSHAccess(_ context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	c.query, c.calls = q, c.calls+1
	if c.wrong {
		q.ResourceUID = "other"
	}
	return substrate.SSHAccessDiscovery{Query: q, BootID: "guest", Status: "absent"}, nil
}

func TestHostSSHDiscoveryUsesCanonicalUserAndProviderIdentity(t *testing.T) {
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 2}}
	for _, scenario := range []string{"valid", "missing-user", "wrong-scope", "foreign-response", "invalid-access"} {
		t.Run(scenario, func(t *testing.T) {
			conn := &sshDiscoveryConnection{wrong: scenario == "foreign-response"}
			d := &Dispatcher{AgentID: "agent", Resolve: func(context.Context, TargetRef) (Target, error) {
				target := Target{Scope: ref.Scope, AgentID: "agent", NodeID: "provider-id", User: "ubuntu", Connection: conn}
				if scenario == "missing-user" {
					target.User = ""
				}
				if scenario == "wrong-scope" {
					target.Scope.Generation++
				}
				return target, nil
			}}
			id := "admin"
			if scenario == "invalid-access" {
				id = "../root"
			}
			response := (&Server{Dispatcher: d}).Handle(context.Background(), Request{Version: 1, Operation: "ssh-discover", Target: ref, SSHAccessID: id})
			if scenario == "valid" {
				require.Empty(t, response.Error)
				require.NotNil(t, response.SSHDiscovery)
				require.Equal(t, "provider-id", conn.query.NodeID)
				require.Equal(t, "ubuntu", conn.query.User)
				require.Equal(t, "web", response.SSHDiscovery.Query.NodeID)
				require.Equal(t, ref.Scope.Generation, conn.query.Generation)
			} else {
				require.NotEmpty(t, response.Error)
				require.Nil(t, response.SSHDiscovery)
				if scenario != "foreign-response" {
					require.Zero(t, conn.calls)
				}
			}
		})
	}
}
