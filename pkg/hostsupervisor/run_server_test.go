package hostsupervisor

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSocketStartLostReplyObserveDoesNotReplay(t *testing.T) {
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
	// StartRun is idempotent even if its first response was lost: the effect
	// callback has run once and the journal is the only completion source.
	require.Equal(t, 1, calls)
	raw, err := NewTaskClient(s.Socket, TargetRef{Topology: req.Topology, Node: "node", Scope: grant.Grant.Scope}).ObserveRun(ctx, req, grant)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	cancel()
	require.NoError(t, <-done)
}
