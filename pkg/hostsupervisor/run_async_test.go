package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/transport"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAsyncRunOutlivesRequestAndNeverReplays(t *testing.T) {
	c, req, grant := runFixture(t)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	request, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var calls atomic.Int32
	observed := make(chan context.Context, 1)
	effect := func(ctx context.Context) (json.RawMessage, error) {
		calls.Add(1)
		observed <- ctx
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	}
	out, err := c.StartAsync(request, service, req, grant, effect)
	require.NoError(t, err)
	require.Equal(t, "running", out.Status)
	effectCtx := <-observed
	cancel()
	require.NoError(t, effectCtx.Err())
	_, err = c.StartAsync(context.Background(), service, req, grant, effect)
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load())
	close(release)
	require.Eventually(t, func() bool {
		out, err := c.ObserveAsync(context.Background(), req, grant)
		return err == nil && out.Status == "completed"
	}, time.Second, time.Millisecond)
}

func asyncLeaseFixture(t *testing.T, budget time.Duration) (*RunCoordinator, RunRequest, controlplane.SignedExecutionGrant, ed25519.PrivateKey) {
	t.Helper()
	c, req, g := runFixture(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.IssuerKey = pub
	now := time.Now()
	g.Grant.ExpiresAt = now.Add(controlplane.ExecutionGrantMaxClockSkew + budget)
	g, err = controlplane.SignExecutionGrant(key, g.Grant, g.BootID, now)
	require.NoError(t, err)
	return c, req, g, key
}

func TestAsyncRunExpiresWithoutRenewalAndColdIntentCannotRestart(t *testing.T) {
	c, req, g, _ := asyncLeaseFixture(t, 100*time.Millisecond)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	canceled := make(chan struct{})
	_, err := c.StartAsync(context.Background(), service, req, g, func(ctx context.Context) (json.RawMessage, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("expired callback still active")
	}
	cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostBootID: c.HostBootID, AgentID: c.AgentID, Authorize: c.Authorize}
	out, err := cold.StartAsync(context.Background(), service, req, g, func(context.Context) (json.RawMessage, error) { t.Error("cold intent replayed"); return nil, nil })
	require.NoError(t, err)
	require.Equal(t, "uncertain", out.Status)
	require.Error(t, cold.Renew(context.Background(), req, g))
}

func TestAsyncRunRenewalSpansInitialBudgetAndRejectsDuplicate(t *testing.T) {
	c, req, g, key := asyncLeaseFixture(t, 180*time.Millisecond)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	release := make(chan struct{})
	started := make(chan context.Context, 1)
	_, err := c.StartAsync(context.Background(), service, req, g, func(ctx context.Context) (json.RawMessage, error) {
		started <- ctx
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return json.RawMessage(`{"renewed":true}`), nil
		}
	})
	require.NoError(t, err)
	effectCtx := <-started
	renewed := g
	renewed.Grant.ExpiresAt = time.Now().UTC().Add(controlplane.ExecutionGrantMaxClockSkew + time.Second)
	renewed, err = controlplane.SignExecutionGrant(key, renewed.Grant, g.BootID, time.Now())
	require.NoError(t, err)
	require.NoError(t, c.Renew(context.Background(), req, renewed))
	require.Error(t, c.Renew(context.Background(), req, renewed))
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-effectCtx.Done():
		t.Fatal("renewed callback stopped at original deadline")
	}
	current, ok := transport.ParentExecutionGrantFromContext(effectCtx)
	require.True(t, ok)
	require.Equal(t, renewed.Grant, current.Grant)
	close(release)
	require.Eventually(t, func() bool {
		out, err := c.ObserveAsync(context.Background(), req, g)
		return err == nil && out.Status == "completed"
	}, time.Second, time.Millisecond)
	gate, err := c.gate(g)
	require.NoError(t, err)
	durable, err := gate.ReadRun(context.Background(), g, req.ActionID)
	require.NoError(t, err)
	require.Equal(t, renewed.Grant, durable.Grant.Grant)
}

func TestAsyncRunUnresponsiveAuthorizationFailsClosed(t *testing.T) {
	c, req, g := runFixture(t)
	c.AuthorizationInterval = 5 * time.Millisecond
	c.AuthorizationTimeout = 20 * time.Millisecond
	var calls atomic.Int32
	unblock := make(chan struct{})
	defer close(unblock)
	c.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		if calls.Add(1) > 1 {
			<-unblock
		}
		return nil
	}
	service, stop := context.WithCancel(context.Background())
	defer stop()
	canceled := make(chan struct{})
	_, err := c.StartAsync(context.Background(), service, req, g, func(ctx context.Context) (json.RawMessage, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("unresponsive authorization did not close authority")
	}
}

func TestAsyncRunCancelLeavesNoncooperativeCallbackUncertain(t *testing.T) {
	c, req, grant := runFixture(t)
	service, stop := context.WithCancel(context.Background())
	defer stop()
	release := make(chan struct{})
	finished := make(chan struct{})
	started := make(chan struct{})
	_, err := c.StartAsync(context.Background(), service, req, grant, func(context.Context) (json.RawMessage, error) {
		close(started)
		defer close(finished)
		<-release
		return json.RawMessage(`{}`), nil
	})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	require.NoError(t, c.Cancel(context.Background(), req, grant))
	out, err := c.ObserveAsync(context.Background(), req, grant)
	require.NoError(t, err)
	require.Equal(t, "uncertain", out.Status)
	require.Error(t, c.Renew(context.Background(), req, grant))
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("released callback did not return")
	}
	require.Eventually(t, func() bool {
		out, err := c.ObserveAsync(context.Background(), req, grant)
		return err == nil && out.Status == "uncertain"
	}, time.Second, time.Millisecond)
}

func TestAsyncRunOnlineRevocationCancels(t *testing.T) {
	c, req, grant := runFixture(t)
	c.AuthorizationInterval = 5 * time.Millisecond
	var revoked atomic.Bool
	c.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}
	service, stop := context.WithCancel(context.Background())
	defer stop()
	canceled := make(chan struct{})
	_, err := c.StartAsync(context.Background(), service, req, grant, func(ctx context.Context) (json.RawMessage, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	revoked.Store(true)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel callback")
	}
}

func TestAsyncRunInvalidResultNeverCreatesCompletion(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`not-json`)} {
		t.Run(string(raw), func(t *testing.T) {
			c, req, g := runFixture(t)
			_, err := c.StartAsync(context.Background(), context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return raw, nil })
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				out, err := c.ObserveAsync(context.Background(), req, g)
				return err == nil && out.Status == "uncertain"
			}, time.Second, time.Millisecond)
		})
	}
}

func TestAsyncRunSlowAdmissionDoesNotBlockOtherRunRenewalOrCancellation(t *testing.T) {
	c, req, g, key := asyncLeaseFixture(t, time.Second)
	blocked := make(chan struct{})
	release := make(chan struct{})
	c.Authorize = func(_ context.Context, proof controlplane.SignedExecutionGrant) error {
		if proof.Grant.ActionID == "other-action" {
			close(blocked)
			<-release
		}
		return nil
	}
	service, stop := context.WithCancel(context.Background())
	defer stop()
	_, err := c.StartAsync(context.Background(), service, req, g, func(ctx context.Context) (json.RawMessage, error) { <-ctx.Done(); return nil, ctx.Err() })
	require.NoError(t, err)
	otherReq := req
	otherReq.ActionID = "other-action"
	otherReq.RunID = "other-run"
	otherReq.TopologyUID = "other-topology"
	otherGrant := g.Grant
	otherGrant.ActionID = otherReq.ActionID
	otherGrant.Scope.TopologyUID = otherReq.TopologyUID
	otherGrant.Digest, err = otherReq.Digest()
	require.NoError(t, err)
	other, err := controlplane.SignExecutionGrant(key, otherGrant, g.BootID, time.Now())
	require.NoError(t, err)
	started := make(chan struct{})
	go func() {
		defer close(started)
		_, _ = c.StartAsync(context.Background(), service, otherReq, other, func(ctx context.Context) (json.RawMessage, error) { <-ctx.Done(); return nil, ctx.Err() })
	}()
	<-blocked
	renewed := g
	renewed.Grant.ExpiresAt = time.Now().UTC().Add(controlplane.ExecutionGrantMaxClockSkew + 2*time.Second)
	renewed, err = controlplane.SignExecutionGrant(key, renewed.Grant, g.BootID, time.Now())
	require.NoError(t, err)
	finished := make(chan error, 1)
	go func() {
		if err := c.Renew(context.Background(), req, renewed); err != nil {
			finished <- err
			return
		}
		finished <- c.Cancel(context.Background(), req, g)
	}()
	var operationErr error
	timely := false
	select {
	case operationErr = <-finished:
		timely = true
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-started
	require.True(t, timely, "unrelated admission blocked live Run control")
	require.NoError(t, operationErr)
}

func TestAsyncRunDeadlineCancelsWhileJournalMutexIsHeld(t *testing.T) {
	c, req, g, _ := asyncLeaseFixture(t, 100*time.Millisecond)
	started := make(chan context.Context, 1)
	_, err := c.StartAsync(context.Background(), context.Background(), req, g, func(ctx context.Context) (json.RawMessage, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	effectCtx := <-started
	active := c.findActive(g)
	active.mu.Lock()
	canceled := false
	select {
	case <-effectCtx.Done():
		canceled = true
	case <-time.After(500 * time.Millisecond):
	}
	active.mu.Unlock()
	<-active.done
	require.True(t, canceled, "deadline cancellation waited for journal mutex")
}

func TestAsyncRunCancelDoesNotWaitForRenewalAuthorization(t *testing.T) {
	c, req, g, key := asyncLeaseFixture(t, time.Second)
	blocked := make(chan struct{})
	release := make(chan struct{})
	c.Authorize = func(_ context.Context, proof controlplane.SignedExecutionGrant) error {
		if proof.Grant.ExpiresAt.After(g.Grant.ExpiresAt) {
			close(blocked)
			<-release
		}
		return nil
	}
	started := make(chan context.Context, 1)
	_, err := c.StartAsync(context.Background(), context.Background(), req, g, func(ctx context.Context) (json.RawMessage, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.NoError(t, err)
	effectCtx := <-started
	active := c.findActive(g)
	renewed := g
	renewed.Grant.ExpiresAt = time.Now().UTC().Add(controlplane.ExecutionGrantMaxClockSkew + 2*time.Second)
	renewed, err = controlplane.SignExecutionGrant(key, renewed.Grant, g.BootID, time.Now())
	require.NoError(t, err)
	renewDone := make(chan error, 1)
	go func() { renewDone <- c.Renew(context.Background(), req, renewed) }()
	<-blocked
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- c.Cancel(context.Background(), req, g) }()
	timely := false
	select {
	case err = <-cancelDone:
		timely = true
	case <-time.After(100 * time.Millisecond):
	}
	canceled := effectCtx.Err() != nil
	close(release)
	require.Error(t, <-renewDone)
	<-active.done
	require.True(t, timely, "cancellation waited for renewal authorization")
	require.NoError(t, err)
	require.True(t, canceled)
}
