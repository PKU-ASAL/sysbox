package persistence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childGrantFixture(t *testing.T, operation ...string) (*ExecutionGrantRepository, controlplane.ExecutionGrant, controlplane.ExecutionGrant, *time.Time) {
	t.Helper()
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	s, err := r.OpenSession(context.Background(), "agent", "host", "")
	require.NoError(t, err)
	op := "run_apply"
	if len(operation) != 0 {
		op = operation[0]
	}
	p, err := r.Issue(context.Background(), controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: s.ID, ActionID: "run", CommandID: "command", Attempt: 1, Operation: op, Digest: "run", TargetBootID: "host"}, time.Minute)
	require.NoError(t, err)
	c := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: p.AgentID, SessionID: p.SessionID, ActionID: "task", CommandID: p.CommandID, Attempt: p.Attempt, Operation: "guest_task", Digest: "task", TargetBootID: "guest"}
	return r, *p, c, &now
}

func TestChildDerivationConcurrentRetryDoesNotRenew(t *testing.T) {
	r, p, c, now := childGrantFixture(t)
	first, err := r.DeriveGuestTask(context.Background(), p, c, 20*time.Second)
	require.NoError(t, err)
	*now = now.Add(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.DeriveGuestTask(context.Background(), p, c, time.Minute)
			if assert.NoError(t, err) {
				assert.Equal(t, first, got)
			}
		}()
	}
	wg.Wait()
}

func TestDerivedSSHChildrenFenceParentAndReceiptReplay(t *testing.T) {
	for _, operation := range []string{"guest_ssh_access_apply", "guest_ssh_access_delete"} {
		t.Run(operation, func(t *testing.T) {
			r, p, c, now := childGrantFixture(t)
			c.Operation = operation
			child, err := r.DeriveGuestTask(context.Background(), p, c, time.Minute)
			require.NoError(t, err)
			require.Equal(t, p.Epoch, child.ParentEpoch)
			require.NoError(t, r.Authorize(context.Background(), *child))
			forged := *child
			forged.ParentEpoch++
			require.Error(t, r.Authorize(context.Background(), forged))
			require.Error(t, r.AcceptHostStop(context.Background(), p, "parent-receipt"))
			*now = now.Add(2 * time.Minute)
			require.Error(t, r.Authorize(context.Background(), *child))
			require.NoError(t, r.AcceptHostStop(context.Background(), *child, "receipt"))
			require.NoError(t, r.AcceptHostStop(context.Background(), *child, "receipt"))
			require.Error(t, r.AcceptHostStop(context.Background(), *child, "different-receipt"))
		})
	}
}

func TestChildDerivationRejectsStaleParentEpochAndSession(t *testing.T) {
	for _, kind := range []string{"epoch", "session"} {
		t.Run(kind, func(t *testing.T) {
			r, p, c, _ := childGrantFixture(t)
			if kind == "epoch" {
				p.Epoch++
			} else {
				_, err := r.OpenSession(context.Background(), p.AgentID, "new-host", p.SessionID)
				require.NoError(t, err)
			}
			_, err := r.DeriveGuestTask(context.Background(), p, c, time.Minute)
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestChildCannotSurviveParentRevocationOrReleaseParent(t *testing.T) {
	for _, operation := range []string{"run_apply", "run_destroy", "run_reset"} {
		t.Run(operation, func(t *testing.T) {
			r, p, c, _ := childGrantFixture(t, operation)
			require.ErrorIs(t, r.RequireHostStopped(context.Background(), p, "parent-stopped"), ErrConflict)
			g, err := r.DeriveGuestTask(context.Background(), p, c, time.Minute)
			require.NoError(t, err)
			require.ErrorIs(t, r.AcceptHostStop(context.Background(), p, "parent-stopped"), ErrConflict)
			require.NoError(t, r.Revoke(context.Background(), p))
			require.ErrorIs(t, r.RequireHostStopped(context.Background(), p, "parent-stopped"), ErrConflict, "revocation does not prove stop")
			require.ErrorIs(t, r.Authorize(context.Background(), *g), ErrConflict)
			_, err = r.Renew(context.Background(), *g, time.Minute)
			require.ErrorIs(t, err, ErrConflict)
			require.NoError(t, r.AcceptHostStop(context.Background(), *g, "child-stopped"))
			require.NoError(t, r.AcceptHostStop(context.Background(), p, "parent-stopped"))
			require.NoError(t, r.RequireHostStopped(context.Background(), p, "parent-stopped"))
			require.ErrorIs(t, r.RequireHostStopped(context.Background(), p, "different"), ErrConflict)
		})
	}
}

func TestTargetedParentCannotBeOverwrittenByChild(t *testing.T) {
	r, p, c, _ := childGrantFixture(t)
	require.NoError(t, r.AcceptHostStop(context.Background(), p, "stop"))
	p.Scope = c.Scope
	next, err := r.Issue(context.Background(), p, time.Minute)
	require.NoError(t, err)
	_, err = r.DeriveGuestTask(context.Background(), *next, c, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	stored, err := r.InspectGrant(context.Background(), p.AgentID, p.Scope)
	require.NoError(t, err)
	require.Equal(t, next, stored)
}

func TestDerivedRenewalRequiresLiveParentAndBoundsDeadline(t *testing.T) {
	r, p, c, now := childGrantFixture(t)
	ctx := context.Background()
	child, err := r.DeriveGuestTask(ctx, p, c, 20*time.Second)
	require.NoError(t, err)
	require.Equal(t, p.Epoch, child.ParentEpoch)
	*now = now.Add(10 * time.Second)
	_, err = r.Renew(ctx, *child, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	renewed, err := r.RenewDerivedGuestTask(ctx, p, *child, time.Minute)
	require.NoError(t, err)
	require.Equal(t, p.ExpiresAt, renewed.ExpiresAt)
	require.Equal(t, child.Epoch, renewed.Epoch)
	bad := p
	bad.Epoch++
	_, err = r.RenewDerivedGuestTask(ctx, bad, *renewed, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	forged := *renewed
	forged.ParentEpoch++
	_, err = r.RenewDerivedGuestTask(ctx, p, forged, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, r.Revoke(ctx, p))
	_, err = r.RenewDerivedGuestTask(ctx, p, *renewed, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func TestStoppedDerivedTaskCannotAllocateAnotherEpoch(t *testing.T) {
	r, p, c, _ := childGrantFixture(t)
	ctx := context.Background()
	child, err := r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.NoError(t, err)
	require.NoError(t, r.AcceptHostStop(ctx, *child, "done"))
	_, err = r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	c.ActionID = "second-task"
	second, err := r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.NoError(t, err)
	require.Greater(t, second.Epoch, child.Epoch)
	_, err = r.DeriveGuestTask(ctx, p, childGrantRequest(*child), time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func childGrantRequest(g controlplane.ExecutionGrant) controlplane.ExecutionGrant {
	g.Epoch = 0
	g.ExpiresAt = time.Time{}
	return g
}

func TestDerivedGrantSurvivesRestartButCannotResurrectExpiry(t *testing.T) {
	r, p, c, now := childGrantFixture(t)
	ctx := context.Background()
	child, err := r.DeriveGuestTask(ctx, p, c, 10*time.Second)
	require.NoError(t, err)
	reopened := NewFilesystemExecutionGrantRepository(r.records.(*fileGuestRecords).root)
	reopened.now = func() time.Time { return *now }
	require.NoError(t, reopened.Authorize(ctx, *child))
	*now = now.Add(11 * time.Second)
	_, err = reopened.RenewDerivedGuestTask(ctx, p, *child, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	*now = now.Add(-time.Second * 10)
	require.ErrorIs(t, reopened.Authorize(ctx, *child), ErrConflict)
}

func TestDerivedRenewalBoundByPresentedParent(t *testing.T) {
	r, p, c, now := childGrantFixture(t)
	ctx := context.Background()
	child, err := r.DeriveGuestTask(ctx, p, c, 30*time.Second)
	require.NoError(t, err)
	*now = now.Add(time.Second)
	latest, err := r.Renew(ctx, p, time.Minute)
	require.NoError(t, err)
	renewed, err := r.RenewDerivedGuestTask(ctx, p, *child, time.Minute)
	require.NoError(t, err)
	require.Equal(t, p.ExpiresAt, renewed.ExpiresAt)
	renewed, err = r.RenewDerivedGuestTask(ctx, *latest, *renewed, time.Minute)
	require.NoError(t, err)
	_, err = r.RenewDerivedGuestTask(ctx, p, *renewed, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	_, err = r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func TestParentIssueRetryWithChildrenIsStable(t *testing.T) {
	r, p, c, now := childGrantFixture(t)
	ctx := context.Background()
	_, err := r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		got, err := r.Issue(ctx, p, time.Minute)
		require.NoError(t, err)
		require.Equal(t, p, *got)
	}
	*now = now.Add(2 * time.Minute)
	p.ExpiresAt = time.Time{}
	_, err = r.Issue(ctx, p, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
}

func TestPrepareHostStopFreezesParentWithoutReleasingScope(t *testing.T) {
	r, p, c, _ := childGrantFixture(t)
	ctx := context.Background()
	require.NoError(t, r.PrepareHostStop(ctx, p, "receipt"))
	require.NoError(t, r.PrepareHostStop(ctx, p, "receipt"))
	stored, err := r.InspectGrant(ctx, p.AgentID, p.Scope)
	require.NoError(t, err)
	require.Equal(t, p.Epoch, stored.Epoch)
	require.Equal(t, controlplane.GrantRevoked, stored.State)
	require.Empty(t, stored.StopReceipt)
	_, err = r.DeriveGuestTask(ctx, p, c, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	next := p
	next.ActionID = "next"
	_, err = r.Issue(ctx, next, time.Minute)
	require.ErrorIs(t, err, ErrConflict)
	// A failed canonical commit can retry preparation without releasing scope.
	require.NoError(t, r.PrepareHostStop(ctx, p, "receipt"))
	require.NoError(t, r.AcceptHostStop(ctx, p, "receipt"))
	successor, err := r.Issue(ctx, next, time.Minute)
	require.NoError(t, err)
	require.NoError(t, r.PrepareHostStop(ctx, p, "receipt"))
	require.NoError(t, r.Authorize(ctx, *successor))
	require.ErrorIs(t, r.PrepareHostStop(ctx, p, "different"), ErrConflict)
}

func TestPrepareHostStopAndDeriveSerialize(t *testing.T) {
	for i := 0; i < 20; i++ {
		r, p, c, _ := childGrantFixture(t)
		ctx := context.Background()
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- r.PrepareHostStop(ctx, p, "receipt") }()
		go func() { <-start; _, err := r.DeriveGuestTask(ctx, p, c, time.Minute); results <- err }()
		close(start)
		first, second := <-results, <-results
		if first == nil {
			require.ErrorIs(t, second, ErrConflict)
		} else {
			require.ErrorIs(t, first, ErrConflict)
			require.NoError(t, second)
		}
	}
}
