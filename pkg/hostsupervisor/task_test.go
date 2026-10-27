package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type taskConnection struct {
	starts, cancels int
	lost            bool
	result          substrate.ExecResult
	handle          substrate.ExecutionHandle
	observe         func(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error)
}

func testRef(req guesttask.Request) TargetRef {
	return TargetRef{Topology: "lab", Node: "web", Scope: req.Grant.Grant.Scope}
}

func (c *taskConnection) Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error) {
	return substrate.ExecResult{}, errors.New("foreground forbidden")
}
func (c *taskConnection) Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	c.starts++
	if c.lost {
		return c.handle, errors.New("reply lost")
	}
	return c.handle, nil
}
func (c *taskConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	if c.observe != nil {
		return c.observe(ctx, h)
	}
	return c.result, nil
}

func TestRecoverySlowActiveRecordDoesNotStarveFollowingRecord(t *testing.T) {
	d, req, conn, key := taskFixtureWithIssuer(t)
	first, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	next := req
	next.ID = "next-task"
	next.Grant.Grant.ActionID = next.ID
	next.Grant.Grant.CommandID = "next-command"
	next.Grant.Grant.Digest, err = next.Digest()
	require.NoError(t, err)
	next.Grant, err = controlplane.SignExecutionGrant(key, next.Grant.Grant, next.Grant.BootID, time.Now())
	require.NoError(t, err)
	conn.handle.ID = next.ID
	conn.handle.Digest = next.Grant.Grant.Digest
	second, err := d.Start(context.Background(), testRef(next), next)
	require.NoError(t, err)
	records := []TaskRecord{first, second}
	sort.Slice(records, func(i, j int) bool { return recordKey(records[i].Handle) < recordKey(records[j].Handle) })
	var observed []string
	conn.observe = func(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
		observed = append(observed, h.ID)
		if h.ID == records[0].Handle.ID {
			<-ctx.Done()
			return substrate.ExecResult{}, ctx.Err()
		}
		return substrate.ExecResult{Status: substrate.ExecStatusRunning}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err = d.Recover(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "slow record reports its own deadline")
	require.NoError(t, ctx.Err(), "one record must not consume the pass lifetime")
	require.Equal(t, []string{records[0].Handle.ID, records[1].Handle.ID}, observed)
}
func (c *taskConnection) CancelExecution(context.Context, substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	c.cancels++
	return substrate.RemoteTerminationUnknown, nil
}
func (c *taskConnection) RenewExecution(context.Context, substrate.ExecutionHandle) error { return nil }
func (c *taskConnection) ReadExecutionOutput(_ context.Context, _ substrate.ExecutionHandle, stream string, cursor int64, limit int) (substrate.ExecutionOutput, error) {
	data := c.result.Stdout
	if stream == "stderr" {
		data = c.result.Stderr
	}
	if cursor >= int64(len(data)) {
		return substrate.ExecutionOutput{NextCursor: cursor}, nil
	}
	end := min(int(cursor)+limit, len(data))
	return substrate.ExecutionOutput{Data: []byte(data[cursor:int64(end)]), NextCursor: int64(end)}, nil
}
func taskFixture(t *testing.T) (*Dispatcher, guesttask.Request, *taskConnection) {
	t.Helper()
	d, req, conn, _ := taskFixtureWithIssuer(t)
	return d, req, conn
}
func taskFixtureWithIssuer(t *testing.T) (*Dispatcher, guesttask.Request, *taskConnection, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := guesttask.Request{ID: "task", UserID: 65534, Args: []string{"true"}, OutputLimit: 1024}
	digest, err := req.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	grant.TargetBootID = "guest-boot"
	req.Grant, err = controlplane.SignExecutionGrant(key, grant, "guest-boot", time.Now())
	require.NoError(t, err)
	conn := &taskConnection{handle: substrate.ExecutionHandle{ID: req.ID, NodeID: "node", Generation: 1, Digest: digest, BootID: "guest-boot", Epoch: 1, TopologyUID: "topology", ResourceUID: "resource"}}
	d := &Dispatcher{Root: t.TempDir(), AgentID: "agent", HostBootID: "host-boot", IssuerKey: pub, HostKey: host, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ReportStop: func(context.Context, controlplane.SignedExecutionStopReceipt) error { return nil }}
	require.NoError(t, os.Chmod(d.Root, 0700))
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: grant.Scope, AgentID: "agent", NodeID: "node", Connection: conn}, nil
	}
	return d, req, conn, key
}
func TestTaskStartLostReplyRecoveredWithoutReplay(t *testing.T) {
	d, req, conn := taskFixture(t)
	conn.lost = true
	first, err := d.Start(context.Background(), testRef(req), req)
	require.Error(t, err)
	require.Equal(t, req.ID, first.Handle.ID)
	reopened := *d
	conn.lost = false
	conn.result = substrate.ExecResult{ExitCodeKnown: true, ExitCode: 7, RemoteTermination: substrate.RemoteTerminationConfirmed}
	result, err := reopened.Observe(context.Background(), testRef(req), first.Handle)
	require.NoError(t, err)
	require.NotNil(t, result.StopReceipt)
	again, err := reopened.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	require.Equal(t, 1, conn.starts)
	require.Equal(t, result.StopReceipt, again.StopReceipt)
}
func TestTaskUnknownCannotMintStopAndWrongProofCannotCancel(t *testing.T) {
	d, req, conn := taskFixture(t)
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	got, err := d.Observe(context.Background(), testRef(req), record.Handle)
	require.NoError(t, err)
	require.Nil(t, got.StopReceipt)
	wrong := record.Handle
	wrong.Epoch++
	_, err = d.Cancel(context.Background(), testRef(req), wrong)
	require.Error(t, err)
	require.Zero(t, conn.cancels)
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 2}, AgentID: "agent", NodeID: "node", Connection: conn}, nil
	}
	_, err = d.Cancel(context.Background(), testRef(req), record.Handle)
	require.Error(t, err)
	require.Zero(t, conn.cancels)
}
func TestStopReceiptRetryIsIdenticalAcrossRestart(t *testing.T) {
	d, req, conn := taskFixture(t)
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	conn.result.RemoteTermination = substrate.RemoteTerminationConfirmed
	var receipts []controlplane.SignedExecutionStopReceipt
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return errors.New("ack lost")
	}
	first, err := d.Observe(context.Background(), testRef(req), record.Handle)
	require.Error(t, err)
	require.NotNil(t, first.StopReceipt)
	reopened := *d
	reopened.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return nil
	}
	second, err := reopened.Observe(context.Background(), testRef(req), record.Handle)
	require.NoError(t, err)
	require.Equal(t, first.StopReceipt, second.StopReceipt)
	require.Equal(t, receipts[0], receipts[1])
}

func TestHostRecoveryRevokedGrantCancelsButDoesNotClaimStop(t *testing.T) {
	d, req, conn := taskFixture(t)
	_, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		return errors.New("authority unavailable")
	}
	called := false
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { called = true; return nil }
	require.NoError(t, d.Recover(context.Background()))
	require.Equal(t, 1, conn.cancels)
	require.False(t, called)
}

func TestHostDispatcherCannotAcceptWorkerEndpointOrForgedTask(t *testing.T) {
	d, req, conn := taskFixture(t)
	wrong := testRef(req)
	wrong.Scope.ResourceUID = "other"
	_, err := d.Start(context.Background(), wrong, req)
	require.Error(t, err)
	require.Zero(t, conn.starts)
	req.Args = []string{"malicious"}
	_, err = d.Start(context.Background(), testRef(req), req)
	require.Error(t, err)
	require.Zero(t, conn.starts)
}

func TestRecoveryDoesNotCancelConcurrentRenewedGrant(t *testing.T) {
	d, req, conn, private := taskFixtureWithIssuer(t)
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	var authorizations atomic.Int32
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		if authorizations.Add(1) > 1 {
			return nil
		}
		// Simulate Renew committing a valid replacement while old authorization
		// was in flight; the stale rejection must not cancel this newer grant.
		grant := req.Grant.Grant
		grant.ExpiresAt = time.Now().Add(2 * time.Minute)
		signed, err := controlplane.SignExecutionGrant(private, grant, record.Handle.BootID, time.Now())
		require.NoError(t, err)
		_, err = d.Renew(context.Background(), testRef(req), record.Handle, signed)
		require.NoError(t, err)
		return errors.New("old authorization rejected")
	}
	require.NoError(t, d.Recover(context.Background()))
	require.Zero(t, conn.cancels)
}

func TestRecoveryReportedAndPendingReceiptDoNotAuthorize(t *testing.T) {
	for _, reported := range []bool{false, true} {
		t.Run(fmt.Sprint(reported), func(t *testing.T) {
			d, req, conn := taskFixture(t)
			record, err := d.Start(context.Background(), testRef(req), req)
			require.NoError(t, err)
			conn.result.RemoteTermination = substrate.RemoteTerminationConfirmed
			d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error {
				if !reported {
					return errors.New("not yet acknowledged")
				}
				return nil
			}
			_, _ = d.Observe(context.Background(), testRef(req), record.Handle)
			var authorizations atomic.Int32
			d.Authorize = func(ctx context.Context, _ controlplane.SignedExecutionGrant) error {
				authorizations.Add(1)
				<-ctx.Done()
				return ctx.Err()
			}
			var reports atomic.Int32
			d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports.Add(1); return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			require.NoError(t, d.Recover(ctx))
			require.Zero(t, authorizations.Load())
			if reported {
				require.Zero(t, reports.Load())
			} else {
				require.EqualValues(t, 1, reports.Load())
			}
		})
	}
}
