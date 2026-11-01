package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestSupervisorShutdownWaitsForRunStorageUsers(t *testing.T) {
	for _, phase := range []string{"execution", "completion-report"} {
		t.Run(phase, func(t *testing.T) {
			coordinator, req, grant := runFixture(t)
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			server := Server{Runs: coordinator}
			if phase == "execution" {
				server.RunEffect = func(ctx context.Context, _ RunRequest) (json.RawMessage, error) {
					close(entered)
					<-ctx.Done()
					close(canceled)
					<-release
					return nil, ctx.Err()
				}
			} else {
				_, key, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				coordinator.HostKey = key
				coordinator.ReportRunID = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
					close(entered)
					<-release
					return nil
				}
				server.RunEffect = func(context.Context, RunRequest) (json.RawMessage, error) {
					return json.RawMessage(`{"id":"run","status":"done"}`), nil
				}
			}
			listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "host.sock"))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- server.serveListener(ctx, listener) }()
			target := TargetRef{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}
			request, stopRequest := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopRequest()
			observed, err := NewTaskClient(listener.Addr().String(), target).StartRun(request, req, grant)
			require.NoError(t, err)
			require.Equal(t, "running", observed.Status)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("Run did not reach blocked phase")
			}
			cancel()
			if phase == "execution" {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("Run was not canceled")
				}
			}
			select {
			case err := <-done:
				t.Fatalf("supervisor returned before %s released storage: %v", phase, err)
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("supervisor did not finish shutdown")
			}
			out, err := coordinator.ObserveAsync(context.Background(), req, grant)
			require.NoError(t, err)
			if phase == "execution" {
				require.Equal(t, "uncertain", out.Status, "shutdown must not certify remote termination")
			} else {
				require.Equal(t, "completed", out.Status, "durable completion survives shutdown")
			}
		})
	}
}

func TestRunChildrenCloseWaitsForLocalObserver(t *testing.T) {
	dispatcher, req, base := taskFixture(t)
	connection := attachRunChild(dispatcher, base, true)
	blocked := &blockedRunChildConnection{runChildConnection: connection, blockObserve: true, entered: make(chan struct{}), release: make(chan struct{})}
	resolve := dispatcher.Resolve
	dispatcher.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = blocked
		return target, err
	}
	children := &RunChildren{Dispatcher: dispatcher}
	_, err := children.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("child observer did not start")
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(blocked.release) }) }
	defer unblock()
	done := make(chan struct{})
	go func() { children.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Close returned while a local child observer still used the dispatcher")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not join the returned child observer")
	}
	_, err = children.Start(context.Background(), testRef(req), req)
	require.Error(t, err, "closing children must seal admission")
}

func TestRunShutdownSealsPendingAdmission(t *testing.T) {
	c, req, grant := runFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	c.PrepareRun = func(ctx context.Context, _ RunRequest) (controlplane.Run, error) {
		close(entered)
		<-release
		return controlplane.Run{}, context.Canceled
	}
	admitted := make(chan error, 1)
	go func() {
		_, err := c.StartAsync(context.Background(), context.Background(), req, grant, func(context.Context) (json.RawMessage, error) {
			t.Error("pending admission executed during shutdown")
			return nil, nil
		})
		admitted <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not start")
	}
	closed := make(chan struct{})
	go func() { c.Shutdown(); close(closed) }()
	select {
	case <-closed:
		t.Error("Shutdown returned with an admission still using storage")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.Error(t, <-admitted)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	_, err := c.StartAsync(context.Background(), context.Background(), req, grant, func(context.Context) (json.RawMessage, error) {
		t.Error("closed coordinator accepted new execution")
		return nil, nil
	})
	require.Error(t, err)
}

func TestRunShutdownWaitsForAuthorizationCallback(t *testing.T) {
	_, _, grant := runFixture(t)
	// A separately bounded check can return before its underlying callback.
	entered, release := make(chan struct{}), make(chan struct{})
	check := &RunCoordinator{Authorize: func(context.Context, controlplane.SignedExecutionGrant) error {
		close(entered)
		<-release
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	checked := make(chan error, 1)
	go func() { checked <- check.authorizeBounded(ctx, grant) }()
	<-entered
	cancel()
	require.ErrorIs(t, <-checked, context.Canceled)
	closed := make(chan struct{})
	go func() { check.Shutdown(); close(closed) }()
	select {
	case <-closed:
		t.Error("Shutdown returned with an authorization callback still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not join authorization callback")
	}
}
