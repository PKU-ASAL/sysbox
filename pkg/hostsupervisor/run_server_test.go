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
	"sync/atomic"
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
	var calls atomic.Int32
	s := &Server{Runs: c, RunEffect: func(context.Context, RunRequest) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"done":true}`), nil
	}}
	request := Request{Version: 1, Operation: "run-start", Target: TargetRef{Topology: req.Topology, Scope: grant.Grant.Scope}, RunRequest: &req, Grant: &grant}
	request.Target.Scope.TopologyUID = "other"
	require.NotEmpty(t, s.Handle(context.Background(), request).Error)
	require.Zero(t, calls.Load())
	request.Target.Scope = grant.Grant.Scope
	require.Empty(t, s.Handle(context.Background(), request).Error)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	// Admission returns before journal completion. Wait for the callback owner
	// to finish before TempDir cleanup can remove its journal directory.
	require.Eventually(t, func() bool { return c.findActive(grant) == nil }, time.Second, time.Millisecond)
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
	var calls atomic.Int32
	s := &Server{Socket: filepath.Join(socketDir, "run.sock"), GroupID: -1, Runs: c, RunEffect: func(context.Context, RunRequest) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	require.Eventually(t, func() bool {
		_, err := NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}).StartRun(ctx, req, grant)
		return err == nil
	}, time.Second, 10*time.Millisecond)
	// Historical observation must not invoke the effect callback again.
	var observed RunObservation
	require.Eventually(t, func() bool {
		var err error
		observed, err = NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}).ObserveRun(ctx, req, grant)
		return err == nil && observed.Status == "completed"
	}, time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
	require.JSONEq(t, `{"ok":true}`, string(observed.Evidence))
	cancel()
	require.NoError(t, <-done)
}

func TestRunOnlyServerRejectsMissingTaskDispatcher(t *testing.T) {
	c, req, grant := runFixture(t)
	s := &Server{Runs: c}
	target := TargetRef{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}
	for _, op := range []string{"info", "start", "observe", "cancel", "renew", "output"} {
		r := s.Handle(context.Background(), Request{Version: 1, Operation: op, Target: target})
		require.Contains(t, r.Error, "unavailable")
	}
	target.Scope.Generation++
	r := s.Handle(context.Background(), Request{Version: 1, Operation: "run-observe", Target: target, RunRequest: &req, Grant: &grant})
	require.Contains(t, r.Error, "mismatch")
}
