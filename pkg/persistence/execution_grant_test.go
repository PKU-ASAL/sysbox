package persistence

import (
	"context"
	"database/sql"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGrantClockRollbackCannotReviveExpiredAuthority(t *testing.T) {
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	s, err := r.OpenSession(ctx, "agent", "boot", "")
	require.NoError(t, err)
	g, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: s.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "apply", Digest: "digest"}, time.Minute)
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)
	require.ErrorIs(t, r.Authorize(ctx, *g), ErrConflict)
	now = now.Add(-2 * time.Minute)
	require.ErrorIs(t, r.Authorize(ctx, *g), ErrConflict)
}

func TestRevokingRunDurablyRevokesChildren(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	r := NewFilesystemExecutionGrantRepository(root)
	session, err := r.OpenSession(ctx, "agent", "boot", "")
	require.NoError(t, err)
	parent, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: "run", Attempt: 1, Operation: "run_apply", Digest: "parent", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	child, err := r.DeriveGuestTask(ctx, *parent, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: "command", ActionID: "task", Attempt: 1, Operation: "guest_task", Digest: "child", TargetBootID: "guest"}, time.Minute)
	require.NoError(t, err)
	require.NoError(t, r.Revoke(ctx, *parent))
	cold := NewFilesystemExecutionGrantRepository(root)
	stored, err := cold.InspectGrant(ctx, "agent", child.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantRevoked, stored.State, "discovery must not depend on a later child authorize call")
	require.NoError(t, cold.AcceptHostStop(ctx, *child, "child-receipt"))
	require.NoError(t, cold.Revoke(ctx, *parent))
	stored, err = cold.InspectGrant(ctx, "agent", child.Scope)
	require.NoError(t, err)
	require.Equal(t, controlplane.GrantStopped, stored.State, "retry cannot reopen a stopped child")
}

func TestSQLiteGrantAuthoritySurvivesRepositoryRestart(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "grants.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	r, err := NewSQLiteExecutionGrantRepository(ctx, db)
	require.NoError(t, err)
	s, err := r.OpenSession(ctx, "agent", "boot", "")
	require.NoError(t, err)
	g, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: s.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "apply", Digest: "digest"}, time.Minute)
	require.NoError(t, err)
	r, err = NewSQLiteExecutionGrantRepository(ctx, db)
	require.NoError(t, err)
	require.NoError(t, r.Authorize(ctx, *g))
	require.NoError(t, r.Revoke(ctx, *g))
	require.NoError(t, r.AcknowledgeStop(ctx, *g, "host receipt"))
	newGrant, err := r.Issue(ctx, *g, time.Minute)
	require.NoError(t, err)
	require.Greater(t, newGrant.Epoch, g.Epoch)
}

func TestGrantExpiryNeverAuthorizesTakeover(t *testing.T) {
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	session, err := r.OpenSession(ctx, "agent", "boot-1", "")
	require.NoError(t, err)
	request := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "exec", Digest: "digest"}
	g, err := r.Issue(ctx, request, time.Minute)
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)
	require.ErrorIs(t, r.Authorize(ctx, *g), ErrConflict)
	request.ActionID = "replacement"
	_, err = r.Issue(ctx, request, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, r.Revoke(ctx, *g))
	_, err = r.Issue(ctx, request, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, r.AcknowledgeStop(ctx, *g, "host-confirmed-no-inflight"))
	next, err := r.Issue(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Greater(t, next.Epoch, g.Epoch)
	require.ErrorIs(t, r.Authorize(ctx, *g), ErrConflict)
	reopened := NewFilesystemExecutionGrantRepository(r.records.(*fileGuestRecords).root)
	require.NoError(t, reopened.Authorize(ctx, *next))
}

func TestGrantSessionCASAndTopologyConflicts(t *testing.T) {
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	s, err := r.OpenSession(ctx, "agent", "boot-1", "")
	require.NoError(t, err)
	g, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: s.ID, ActionID: "action", CommandID: "command", Attempt: 1, Operation: "apply", Digest: "digest"}, time.Minute)
	require.NoError(t, err)
	_, err = r.OpenSession(ctx, "agent", "boot-2", "")
	require.ErrorIs(t, err, ErrConflict)
	next, err := r.OpenSession(ctx, "agent", "boot-2", s.ID)
	require.NoError(t, err)
	require.ErrorIs(t, r.Authorize(ctx, *g), ErrConflict)
	_, err = r.Renew(ctx, *g, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	child := *g
	child.SessionID = next.ID
	child.Scope.ResourceUID = "node"
	child.Scope.Generation = 1
	_, err = r.Issue(ctx, child, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func TestRunGrantDerivesDurableGuestTaskGrant(t *testing.T) {
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	session, err := r.OpenSession(ctx, "agent", "host", "")
	require.NoError(t, err)
	parent, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, ActionID: "run", CommandID: "command", Attempt: 1, Operation: "run_apply", Digest: "run-digest", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	child := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: session.ID, ActionID: "task", CommandID: "command", Attempt: 1, Operation: "guest_task", Digest: "task-digest", TargetBootID: "guest"}
	derived, err := r.DeriveGuestTask(ctx, *parent, child, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "guest_task", derived.Operation)
	require.Greater(t, derived.Epoch, parent.Epoch)
	stored, err := r.InspectGrant(ctx, "agent", child.Scope)
	require.NoError(t, err)
	require.Equal(t, derived.Epoch, stored.Epoch)
	bad := child
	bad.Digest = "different"
	_, err = r.DeriveGuestTask(ctx, *parent, bad, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func TestConcurrentGrantIssueHasOneWinner(t *testing.T) {
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	s, err := r.OpenSession(ctx, "agent", "boot", "")
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: s.ID, ActionID: string(rune('a' + i)), CommandID: "command", Attempt: 1, Operation: "apply", Digest: "digest"}, time.Minute)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else {
			require.True(t, errors.Is(err, ErrConflict))
		}
	}
	require.Equal(t, 1, won)
}
