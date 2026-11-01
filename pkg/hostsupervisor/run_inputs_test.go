package hostsupervisor

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestRunInputsReachHostButNotAdmissionOrCheckpoint(t *testing.T) {
	c, req, g := runFixture(t)
	c.PrepareRun = func(ctx context.Context, _ RunRequest) (controlplane.Run, error) {
		require.Equal(t, "secret-canary", RunInputs(ctx)["flag"])
		return controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, Op: req.Action, Target: req.Target, AgentID: c.AgentID, Status: controlplane.RunRunning, QueuedAt: time.Now(), Attempt: 1, LeaseOwner: "lease"}, nil
	}
	reached := make(chan map[string]string, 1)
	server := Server{Runs: c, RunEffect: func(ctx context.Context, _ RunRequest) (json.RawMessage, error) {
		reached <- RunInputs(ctx)
		return json.RawMessage(`{"ok":true}`), nil
	}}
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "host.sock"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.serveListener(ctx, listener) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	target := TargetRef{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}
	_, err = NewTaskClient(listener.Addr().String(), target).StartRun(WithRunInputs(ctx, map[string]string{"flag": "secret-canary"}), req, g)
	require.NoError(t, err)
	select {
	case inputs := <-reached:
		require.Equal(t, "secret-canary", inputs["flag"])
	case <-time.After(time.Second):
		t.Fatal("missing host input")
	}
	gate, err := c.gate(g)
	require.NoError(t, err)
	record, err := gate.ReadRun(ctx, g, req.ActionID)
	require.NoError(t, err)
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-canary")
}
