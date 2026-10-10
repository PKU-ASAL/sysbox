package hostsupervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

type runChildConnection struct {
	*taskConnection
	finished      atomic.Bool
	cancelCount   atomic.Int64
	renewCount    atomic.Int64
	confirmCancel bool
	exitCode      int
	unknownExit   bool
}

type slowPollingConnection struct {
	*runChildConnection
	firstReturn time.Time
	secondStart time.Time
	calls       int
}

type slowCleanupConnection struct {
	*runChildConnection
	calls                    int
	secondReturn, thirdStart time.Time
}

func (c *slowCleanupConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	c.calls++
	if c.calls == 2 {
		time.Sleep(100 * time.Millisecond)
		c.secondReturn = time.Now()
	}
	if c.calls == 3 {
		c.thirdStart = time.Now()
		c.finished.Store(true)
	}
	return c.runChildConnection.ObserveExecution(ctx, h)
}
func TestRunChildCleanupLeavesUnlockedIntervalAfterSlowObservation(t *testing.T) {
	d, req, base := taskFixture(t)
	conn := &slowCleanupConnection{runChildConnection: &runChildConnection{taskConnection: base}}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = conn
		return target, err
	}
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	children := &RunChildren{Dispatcher: d}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, children.cleanup(ctx, record))
	require.GreaterOrEqual(t, conn.thirdStart.Sub(conn.secondReturn), 40*time.Millisecond, "cleanup must leave time for independent recovery")
}

func (c *slowPollingConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	c.calls++
	if c.calls == 1 {
		time.Sleep(150 * time.Millisecond) // exceed the old fixed ticker period
		c.firstReturn = time.Now()
		return substrate.ExecResult{Status: substrate.ExecStatusRunning}, nil
	}
	c.secondStart = time.Now()
	c.finished.Store(true)
	return c.runChildConnection.ObserveExecution(ctx, h)
}

func TestRunChildSlowObservationLeavesAnUnlockedPollingInterval(t *testing.T) {
	d, req, base := taskFixture(t)
	wrapper := &slowPollingConnection{runChildConnection: &runChildConnection{taskConnection: base}}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = wrapper
		return target, err
	}
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	children := &RunChildren{Dispatcher: d}
	require.NoError(t, children.observe(context.Background(), record))
	require.GreaterOrEqual(t, wrapper.secondStart.Sub(wrapper.firstReturn), 80*time.Millisecond, "slow observations must not immediately reacquire the shared task lock")
}

func (c *runChildConnection) ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error) {
	if c.finished.Load() || c.confirmCancel && c.cancelCount.Load() > 0 {
		return substrate.ExecResult{ExitCodeKnown: !c.unknownExit, ExitCode: c.exitCode, RemoteTermination: substrate.RemoteTerminationConfirmed}, nil
	}
	return substrate.ExecResult{Status: substrate.ExecStatusRunning, RemoteTermination: substrate.RemoteTerminationUnknown}, nil
}

type blockedRunChildConnection struct {
	*runChildConnection
	startID      string
	blockObserve bool
	entered      chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (c *blockedRunChildConnection) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	id, grant, _ := transport.TaskGrantFromContext(ctx)
	if id != c.startID {
		return c.runChildConnection.Start(ctx, req)
	}
	c.once.Do(func() { close(c.entered) })
	<-c.release // Deliberately model a provider that ignores its context.
	h := c.handle
	h.ID, h.Digest = id, grant.Grant.Digest
	return h, nil
}

func (c *blockedRunChildConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	if c.blockObserve {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	return c.runChildConnection.ObserveExecution(ctx, h)
}

func TestRunChildrenBlockedStartDoesNotBlockSiblingCancellation(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	conn := attachRunChild(d, base, true)
	blocked := &blockedRunChildConnection{runChildConnection: conn, startID: "blocked", entered: make(chan struct{}), release: make(chan struct{})}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = blocked
		return target, err
	}
	children := &RunChildren{Dispatcher: d}
	_, err := children.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	next := req
	next.ID = "blocked"
	next.Grant.Grant.ActionID = next.ID
	next.Grant.Grant.Digest, err = next.Digest()
	require.NoError(t, err)
	next.Grant, err = controlplane.SignExecutionGrant(issuer, next.Grant.Grant, next.Grant.BootID, time.Now())
	require.NoError(t, err)
	started := make(chan error, 1)
	go func() { _, err := children.Start(context.Background(), testRef(next), next); started <- err }()
	<-blocked.entered
	defer func() {
		close(blocked.release)
		<-started
		_ = children.Wait(context.Background())
	}()
	canceled := make(chan error, 1)
	go func() { canceled <- children.Cancel(context.Background()) }()
	require.Eventually(t, func() bool { return conn.cancelCount.Load() > 0 }, 500*time.Millisecond, 5*time.Millisecond)
	select {
	case err := <-canceled:
		require.ErrorContains(t, err, "unconfirmed")
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel blocked behind provider Start")
	}
}

func TestRunChildrenNoncooperativeObserveCannotHangWait(t *testing.T) {
	d, req, base := taskFixture(t)
	conn := attachRunChild(d, base, true)
	blocked := &blockedRunChildConnection{runChildConnection: conn, blockObserve: true, entered: make(chan struct{}), release: make(chan struct{})}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = blocked
		return target, err
	}
	children := &RunChildren{Dispatcher: d}
	h, err := children.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	<-blocked.entered
	defer func() {
		close(blocked.release)
		_ = children.Wait(context.Background())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- children.Wait(ctx) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorContains(t, err, "unconfirmed")
	case <-time.After(3 * time.Second):
		t.Fatal("Wait blocked behind provider Observe")
	}
	record, err := d.load(h)
	require.NoError(t, err)
	require.Nil(t, record.StopReceipt)
}

func TestRunChildrenWaitReportsFailedAndUnknownExit(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "unknown"}[unknown], func(t *testing.T) {
			d, req, base := taskFixture(t)
			conn := attachRunChild(d, base, true)
			conn.exitCode, conn.unknownExit = 7, unknown
			conn.finished.Store(true)
			children := &RunChildren{Dispatcher: d}
			h, err := children.Start(context.Background(), testRef(req), req)
			require.NoError(t, err)
			err = children.Wait(context.Background())
			if unknown {
				require.ErrorContains(t, err, "unknown exit")
			} else {
				require.ErrorContains(t, err, "status 7")
			}
			record, err := d.load(h)
			require.NoError(t, err)
			require.NotNil(t, record.StopReceipt)
			require.True(t, record.Reported)
			require.NoError(t, children.ProveStopped(context.Background()))
		})
	}
}
func (c *runChildConnection) CancelExecution(context.Context, substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	c.cancelCount.Add(1)
	return substrate.RemoteTerminationUnknown, nil
}
func (c *runChildConnection) RenewExecution(context.Context, substrate.ExecutionHandle) error {
	c.renewCount.Add(1)
	return nil
}

func attachRunChild(d *Dispatcher, conn *taskConnection, confirm bool) *runChildConnection {
	wrapper := &runChildConnection{taskConnection: conn, confirmCancel: confirm}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = wrapper
		return target, err
	}
	return wrapper
}

func TestRunChildrenWaitNaturalCompletionAndDeduplicate(t *testing.T) {
	d, req, base := taskFixture(t)
	conn := attachRunChild(d, base, true)
	children := &RunChildren{Dispatcher: d}
	var starts sync.WaitGroup
	for range 12 {
		starts.Add(1)
		go func() {
			defer starts.Done()
			_, err := children.Start(context.Background(), testRef(req), req)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	starts.Wait()
	done := make(chan error, 1)
	go func() { done <- children.Wait(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("active service unexpectedly completed: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	require.Zero(t, conn.cancelCount.Load())
	conn.finished.Store(true)
	require.NoError(t, <-done)
	require.Equal(t, 1, base.starts)
	require.Len(t, children.children, 1)
	require.Zero(t, conn.cancelCount.Load())
}

func TestRunChildrenParentCancellationReconcilesIndependently(t *testing.T) {
	d, req, base := taskFixture(t)
	conn := attachRunChild(d, base, true)
	children := &RunChildren{Dispatcher: d}
	ctx, cancel := context.WithCancel(context.Background())
	h, err := children.Start(ctx, testRef(req), req)
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, children.Wait(ctx), context.Canceled)
	require.EqualValues(t, 1, conn.cancelCount.Load())
	record, err := d.load(h)
	require.NoError(t, err)
	require.NotNil(t, record.StopReceipt)
	require.True(t, record.Reported)
}

func TestRunChildrenProveStoppedSeparatesCancellationFromEvidence(t *testing.T) {
	d, req, base := taskFixture(t)
	attachRunChild(d, base, true)
	children := &RunChildren{Dispatcher: d}
	ctx, cancel := context.WithCancel(context.Background())
	h, err := children.Start(ctx, testRef(req), req)
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, children.Wait(ctx), context.Canceled)
	require.NoError(t, children.ProveStopped(context.Background()))
	record, err := d.load(h)
	require.NoError(t, err)
	record.Reported = false
	require.NoError(t, d.save(record))
	require.Error(t, children.ProveStopped(context.Background()))
	record.Reported = true
	record.StopReceipt.Signature = []byte("invalid")
	require.NoError(t, d.save(record))
	require.Error(t, children.ProveStopped(context.Background()))
}

func TestRunChildrenProveStoppedRejectsHandlelessAdmission(t *testing.T) {
	d, req, _ := taskFixture(t)
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{}, errors.New("resolve failed after authorization")
	}
	children := &RunChildren{Dispatcher: d}
	h, err := children.Start(context.Background(), testRef(req), req)
	require.Error(t, err)
	require.Empty(t, h.ID)
	require.Error(t, children.ProveStopped(context.Background()))
}

func TestRunChildrenProveStoppedRejectsChangedParentIdentity(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	conn := attachRunChild(d, base, true)
	conn.finished.Store(true)
	children := &RunChildren{Dispatcher: d}
	h, err := children.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	require.NoError(t, children.Wait(context.Background()))
	record, err := d.load(h)
	require.NoError(t, err)
	record.Grant.Grant.CommandID = "other-command"
	record.Grant, err = controlplane.SignExecutionGrant(issuer, record.Grant.Grant, record.Grant.BootID, time.Now())
	require.NoError(t, err)
	receipt, err := controlplane.SignExecutionTaskStopReceipt(d.HostKey, record.Grant.Grant, d.HostBootID, h.BootID, h.ID, h.Digest, controlplane.GuestTaskResult{Result: controlplane.GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "changed-parent", time.Now())
	require.NoError(t, err)
	record.StopReceipt = &receipt
	require.NoError(t, d.save(record))
	require.Error(t, children.ProveStopped(context.Background()))
}

func TestRunChildrenProveStoppedRejectsPendingAdmission(t *testing.T) {
	d, req, base := taskFixture(t)
	conn := attachRunChild(d, base, true)
	blocked := &blockedRunChildConnection{runChildConnection: conn, startID: req.ID, entered: make(chan struct{}), release: make(chan struct{})}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = blocked
		return target, err
	}
	children := &RunChildren{Dispatcher: d}
	started := make(chan error, 1)
	go func() { _, err := children.Start(context.Background(), testRef(req), req); started <- err }()
	<-blocked.entered
	defer func() {
		close(blocked.release)
		<-started
		_ = children.Cancel(context.Background())
	}()
	require.Error(t, children.ProveStopped(context.Background()))
	_, err := children.Start(context.Background(), testRef(req), req)
	require.ErrorContains(t, err, "closed")
}

func TestRunChildrenUnknownCleanupPreservesJournal(t *testing.T) {
	d, req, base := taskFixture(t)
	attachRunChild(d, base, false)
	children := &RunChildren{Dispatcher: d}
	h, err := children.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	err = children.Cancel(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	record, err := d.load(h)
	require.NoError(t, err)
	require.True(t, record.CancelRequested)
	require.Nil(t, record.StopReceipt)
}

func TestRunChildrenLostStartReplyStillCanceled(t *testing.T) {
	d, req, base := taskFixture(t)
	base.lost = true
	conn := attachRunChild(d, base, true)
	children := &RunChildren{Dispatcher: d}
	h, err := children.Start(context.Background(), testRef(req), req)
	require.ErrorContains(t, err, "reply lost")
	require.NotEmpty(t, h.ID)
	require.ErrorContains(t, children.Wait(context.Background()), "reply lost")
	require.EqualValues(t, 1, conn.cancelCount.Load())
	record, err := d.load(h)
	require.NoError(t, err)
	require.NotNil(t, record.StopReceipt)
}

func TestRunChildrenRenewUsesCurrentParent(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	conn := attachRunChild(d, base, true)
	now := time.Now()
	req.Grant.Grant.ExpiresAt = now.Add(1400 * time.Millisecond)
	var err error
	req.Grant, err = controlplane.SignExecutionGrant(issuer, req.Grant.Grant, req.Grant.BootID, now)
	require.NoError(t, err)
	var parentVersion atomic.Uint64
	parentVersion.Store(1)
	ctx, cancel := context.WithCancel(transport.WithParentExecutionGrantResolver(context.Background(), func() (controlplane.SignedExecutionGrant, bool) {
		parent := req.Grant
		parent.Grant.Epoch = parentVersion.Load()
		return parent, true
	}))
	defer cancel()
	renewed := make(chan uint64, 8)
	children := &RunChildren{Dispatcher: d, RenewGrant: func(_ context.Context, parent, child controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
		renewed <- parent.Grant.Epoch
		child.Grant.ExpiresAt = time.Now().Add(1400 * time.Millisecond)
		return controlplane.SignExecutionGrant(issuer, child.Grant, child.BootID, time.Now())
	}}
	_, err = children.Start(ctx, testRef(req), req)
	require.NoError(t, err)
	awaitRenewal := func() uint64 {
		t.Helper()
		select {
		case epoch := <-renewed:
			return epoch
		case <-time.After(3 * time.Second):
			t.Fatal("child not renewed")
			return 0
		}
	}
	require.EqualValues(t, 1, awaitRenewal())
	parentVersion.Store(2)
	require.EqualValues(t, 2, awaitRenewal())
	cancel()
	require.True(t, errors.Is(children.Wait(context.Background()), context.Canceled))
	require.Positive(t, conn.renewCount.Load())
}
