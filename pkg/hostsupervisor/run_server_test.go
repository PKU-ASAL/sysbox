package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTopologyRunDispatchRequiresMatchingSignedScope(t *testing.T) {
	c, req, grant := runFixture(t)
	req.ResourceUID, req.Target, req.Generation, req.ExpectedSerial = "", "", 0, 0
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.IssuerKey = pub
	grant.Grant.Scope.ResourceUID, grant.Grant.Scope.Generation = "", 0
	grant.Grant.Digest, err = req.Digest()
	require.NoError(t, err)
	grant, err = controlplane.SignExecutionGrant(key, grant.Grant, c.HostBootID, time.Now())
	require.NoError(t, err)
	calls := 0
	s := &Server{Runs: c, RunEffect: func(context.Context, RunRequest) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"done":true}`), nil
	}}
	request := Request{Version: 1, Operation: "run-start", Target: TargetRef{Topology: req.Topology, Scope: grant.Grant.Scope}, RunRequest: &req, Grant: &grant}
	request.Target.Scope.TopologyUID = "other"
	require.NotEmpty(t, s.Handle(context.Background(), request).Error)
	require.Zero(t, calls)
	request.Target.Scope = grant.Grant.Scope
	require.Empty(t, s.Handle(context.Background(), request).Error)
	require.Equal(t, 1, calls)
}

func TestRunBootDiscoveryDoesNotRequireExistingNode(t *testing.T) {
	s := &Server{Runs: &RunCoordinator{HostBootID: "host"}}
	target := TargetRef{Topology: "lab", Scope: controlplane.ExecutionScope{TopologyUID: "uid"}}
	got := s.Handle(context.Background(), Request{Version: 1, Operation: "run-info", Target: target})
	require.Empty(t, got.Error)
	require.Equal(t, "host", got.BootID)
	for _, operation := range []string{"host-info", "start", "power-apply", "gc-delete"} {
		got = s.Handle(context.Background(), Request{Version: 1, Operation: operation, Target: target})
		require.NotEmpty(t, got.Error)
	}
}

func TestRunSocketStartAndHistoricalObserve(t *testing.T) {
	c, req, grant := runFixture(t)
	require.NoError(t, os.Chmod(c.Root, 0700))
	socketDir := t.TempDir()
	require.NoError(t, os.Chmod(socketDir, 0700))
	calls := 0
	s := &Server{Socket: filepath.Join(socketDir, "run.sock"), GroupID: -1, Runs: c, RunEffect: func(context.Context, RunRequest) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"ok":true}`), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	require.Eventually(t, func() bool {
		_, err := NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: "node", Scope: grant.Grant.Scope}).StartRun(ctx, req, grant)
		return err == nil
	}, time.Second, 10*time.Millisecond)
	// Historical observation must not invoke the effect callback again.
	require.Equal(t, 1, calls)
	raw, err := NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: "node", Scope: grant.Grant.Scope}).ObserveRun(ctx, req, grant)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	cancel()
	require.NoError(t, <-done)
}

func TestRunOnlyServerRejectsMissingTaskDispatcher(t *testing.T) {
	c, req, grant := runFixture(t)
	s := &Server{Runs: c}
	target := TargetRef{Topology: req.Topology, Node: "node", Scope: grant.Grant.Scope}
	for _, op := range []string{"info", "start", "observe", "cancel", "renew", "output"} {
		r := s.Handle(context.Background(), Request{Version: 1, Operation: op, Target: target})
		require.Contains(t, r.Error, "unavailable")
	}
	target.Scope.Generation++
	r := s.Handle(context.Background(), Request{Version: 1, Operation: "run-observe", Target: target, RunRequest: &req, Grant: &grant})
	require.Contains(t, r.Error, "mismatch")
}
