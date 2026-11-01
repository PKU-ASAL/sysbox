package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestHostObservationServerJoinsCollectorBeforeShutdown(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "host.sock"))
	require.NoError(t, err)
	server := &Server{Observer: &Observer{AgentID: "agent", BootID: "boot", Key: key, Collect: func(ctx context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
		close(entered)
		<-ctx.Done()
		<-release
		close(finished)
		return controlplane.AgentInventory{}, nil, ctx.Err()
	}}}
	done := make(chan error, 1)
	go func() { done <- server.serveListener(ctx, listener) }()
	clientDone := make(chan error, 1)
	go func() {
		_, err := NewTaskClient(listener.Addr().String(), TargetRef{}).HostObservation(context.Background(), ObservationRequest{SessionID: "session"})
		clientDone <- err
	}()
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("host returned while collector was using storage")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	<-finished
	require.Error(t, <-clientDone)
	server.Observer.Collect = func(context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
		return controlplane.AgentInventory{}, nil, nil
	}
	o, err := server.Observer.Snapshot(context.Background(), ObservationRequest{SessionID: "session"})
	require.NoError(t, err)
	require.NoError(t, controlplane.VerifyHostObservation(public, o))
}
