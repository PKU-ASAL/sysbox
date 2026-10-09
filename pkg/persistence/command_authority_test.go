package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCommandAuthorityHistoryIncludesStoppedAndSupersededGrant(t *testing.T) {
	ctx := context.Background()
	repo := NewFilesystemExecutionGrantRepository(t.TempDir())
	history, err := repo.HasCommandExecutionHistory(ctx, "agent", "command")
	require.NoError(t, err)
	require.False(t, history)
	session, err := repo.OpenSession(ctx, "agent", "session", "")
	require.NoError(t, err)
	req := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, ActionID: "op", CommandID: "command", Attempt: 1, Operation: "ssh_access_apply", Digest: "digest", TargetBootID: "host"}
	grant, err := repo.Issue(ctx, req, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repo.AcceptHostStop(ctx, *grant, "verified-stop"))
	req.CommandID = "successor"
	req.ActionID = "next"
	_, err = repo.Issue(ctx, req, time.Minute)
	require.NoError(t, err)
	history, err = repo.HasCommandExecutionHistory(ctx, "agent", "command")
	require.NoError(t, err)
	require.True(t, history)
}
