package hostsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestStopOnlyRecordPinsProviderBeforeCancellation(t *testing.T) {
	d, req, conn, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	stop := controlplane.GuestTaskStop{Topology: "lab", Node: "web", Cancellation: proof}
	pending, err := d.StopAuthorized(context.Background(), stop)
	require.NoError(t, err)
	require.Equal(t, "node", pending.ProviderNodeID)
	require.Nil(t, pending.StopReceipt)
	require.Equal(t, 1, conn.cancels)
	resolve := d.Resolve
	cold := *d
	cold.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.NodeID = "replacement"
		target.Connection = &rebootedStopConnection{taskConnection: conn}
		return target, err
	}
	_, err = cold.StopAuthorized(context.Background(), stop)
	require.ErrorIs(t, err, guesttask.ErrConflict)
	require.Equal(t, 1, conn.cancels, "replacement must not receive cancellation")
	record, err := cold.load(pending.Handle)
	require.NoError(t, err)
	require.Nil(t, record.StopReceipt, "replacement boot must not prove original task stopped")
}

func TestStopOnlyRecoveryPreservesCompletedResultAndReplaysExactReceipt(t *testing.T) {
	d, req, conn, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	conn.result = substrate.ExecResult{ExitCodeKnown: true, ExitCode: 7, Stdout: "finished", RemoteTermination: substrate.RemoteTerminationConfirmed}
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { return errors.New("ack lost") }
	stop := controlplane.GuestTaskStop{Topology: "lab", Node: "web", Cancellation: proof}
	record, err := d.StopAuthorized(context.Background(), stop)
	require.ErrorContains(t, err, "ack lost")
	require.NotNil(t, record.StopReceipt)
	require.Equal(t, "exit", record.StopReceipt.GuestResult.ResultClass)
	require.Equal(t, 7, record.StopReceipt.GuestResult.Result.ExitCode)
	require.Equal(t, "ZmluaXNoZWQ=", record.StopReceipt.GuestResult.Result.Stdout)
	want, err := json.Marshal(record.StopReceipt)
	require.NoError(t, err)
	cold := *d
	cold.Resolve = func(context.Context, TargetRef) (Target, error) {
		t.Fatal("sealed receipt recovery must not contact provider")
		return Target{}, nil
	}
	reports := 0
	cold.ReportStop = func(_ context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
		reports++
		got, err := json.Marshal(receipt)
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got))
		return nil
	}
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
	require.Zero(t, conn.starts)
	require.Equal(t, 1, conn.cancels)
	// Rediscovery after a lost API acknowledgement must resend the same receipt.
	_, err = cold.StopAuthorized(context.Background(), stop)
	require.NoError(t, err)
	require.Equal(t, 2, reports)
}

func TestStopOnlyRecordRejectsOldFormatAndTamperedProof(t *testing.T) {
	for _, corruption := range []string{"old-format", "signature"} {
		t.Run(corruption, func(t *testing.T) {
			d, req, _, issuer := taskFixtureWithIssuer(t)
			g := req.Grant.Grant
			g.State = controlplane.GrantRevoked
			proof, err := controlplane.SignExecutionCancellation(issuer, g)
			require.NoError(t, err)
			r, err := d.StopAuthorized(context.Background(), controlplane.GuestTaskStop{Topology: "lab", Node: "web", Cancellation: proof})
			require.NoError(t, err)
			if corruption == "old-format" {
				r.Version = 2
			} else {
				r.Cancellation.Signature[0] ^= 1
			}
			require.NoError(t, d.save(r))
			_, err = d.load(r.Handle)
			require.ErrorIs(t, err, guesttask.ErrConflict)
		})
	}
}

type signedStopConnection struct {
	*taskConnection
	onStop func(context.Context, substrate.ExecutionHandle)
}

type rebootedStopConnection struct{ *taskConnection }

func TestRunTaskStopRecoveryUsesSignedRunSource(t *testing.T) {
	d, req, conn, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.ParentEpoch = 10
	g.Epoch = 11
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignRunExecutionCancellation(issuer, g, "parent-run", "lab")
	require.NoError(t, err)
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		t.Fatal("child must use Run resolver")
		return Target{}, nil
	}
	d.FindRunTaskRef = func(_ context.Context, topology, runID string, scope controlplane.ExecutionScope) (TargetRef, error) {
		require.Equal(t, "parent-run", runID)
		require.Equal(t, "lab", topology)
		require.Equal(t, g.Scope, scope)
		return TargetRef{Topology: topology, Node: "web", Scope: scope}, nil
	}
	d.ResolveRun = func(_ context.Context, ref TargetRef, runID string) (Target, error) {
		require.Equal(t, "parent-run", runID)
		return Target{Scope: ref.Scope, AgentID: g.AgentID, NodeID: "node", Connection: conn}, nil
	}
	stop := controlplane.GuestTaskStop{Topology: "lab", Cancellation: proof}
	r, err := d.StopAuthorized(context.Background(), stop)
	require.NoError(t, err)
	require.Equal(t, "parent-run", r.RunID)
	require.Equal(t, "web", r.Ref.Node)
	require.True(t, r.CancelRequested)
	require.Zero(t, conn.starts)
	cold := *d
	cold.FindRunTaskRef = nil // local recovery uses the persisted logical target
	conn.result.RemoteTermination = substrate.RemoteTerminationConfirmed
	require.NoError(t, cold.Recover(context.Background()))
	completed, err := cold.load(r.Handle)
	require.NoError(t, err)
	require.True(t, completed.Reported)
	require.Equal(t, g.ParentEpoch, completed.StopReceipt.Grant.ParentEpoch)
	// Parent checkpoints retain the original active credential, while discovery
	// rebuilt the child from revoked stop-only authority. Identity is unchanged.
	active := g
	active.State = controlplane.GrantActive
	original, err := controlplane.SignExecutionGrant(issuer, active, active.TargetBootID, req.Grant.IssuedAt)
	require.NoError(t, err)
	parent := cold
	parent.RunID = proof.RunID
	require.NoError(t, recoverCheckpointChild(context.Background(), &parent, runCheckpointChild{Ref: r.Ref, Handle: r.Handle, Grant: original}))
	admitted := r
	admitted.Grant = original
	children := &RunChildren{Dispatcher: &parent, children: map[string]TaskRecord{recordKey(r.Handle): admitted}}
	require.NoError(t, children.ProveStopped(context.Background()))
	for _, field := range []string{"epoch", "parent_epoch"} {
		mismatched := original
		if field == "epoch" {
			mismatched.Grant.Epoch++
		} else {
			mismatched.Grant.ParentEpoch++
		}
		require.Error(t, recoverCheckpointChild(context.Background(), &parent, runCheckpointChild{Ref: r.Ref, Handle: r.Handle, Grant: mismatched}), field)
		admitted.Grant = mismatched
		children.children[recordKey(r.Handle)] = admitted
		require.Error(t, children.ProveStopped(context.Background()), field)
	}
	completed.RunID = "foreign-run"
	require.NoError(t, cold.save(completed))
	_, err = cold.load(r.Handle)
	require.ErrorIs(t, err, guesttask.ErrConflict)
}

func (c *rebootedStopConnection) GuestTaskBootID(context.Context) (string, error) {
	return "new-guest-boot", nil
}

func TestMissingHostTaskAfterGuestRebootReportsUnknownExit(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = &rebootedStopConnection{taskConnection: base}
		return target, err
	}
	ref := testRef(req)
	record, err := d.StopAuthorized(context.Background(), controlplane.GuestTaskStop{Topology: ref.Topology, Node: ref.Node, Cancellation: proof})
	require.NoError(t, err)
	require.True(t, record.Reported)
	require.NotNil(t, record.StopReceipt)
	require.Equal(t, "new-guest-boot", record.StopReceipt.ObservedGuestBootID)
	require.Equal(t, "unknown", record.StopReceipt.GuestResult.ResultClass)
	require.False(t, record.StopReceipt.GuestResult.ExitCodeKnown)
	require.Zero(t, base.starts)
	require.Zero(t, base.cancels)
	_, err = d.load(record.Handle)
	require.NoError(t, err)
}

func requireSameStopProof(t *testing.T, want, got controlplane.SignedExecutionCancellation) {
	t.Helper()
	a, err := json.Marshal(want)
	require.NoError(t, err)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(a), string(b))
}

func TestStopOnlyRecordSurvivesUnresolvedProviderAndBlocksLateStart(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	ref := testRef(req)
	stop := controlplane.GuestTaskStop{Topology: ref.Topology, Node: ref.Node, Cancellation: proof}
	resolve := d.Resolve
	d.Resolve = func(context.Context, TargetRef) (Target, error) { return Target{}, errors.New("provider unavailable") }
	pending, err := d.StopAuthorized(context.Background(), stop)
	require.ErrorContains(t, err, "provider unavailable")
	require.Empty(t, pending.ProviderNodeID)
	durable, err := d.load(pending.Handle)
	require.NoError(t, err)
	require.True(t, durable.CancelRequested)
	conn := &signedStopConnection{taskConnection: base}
	conn.onStop = func(ctx context.Context, _ substrate.ExecutionHandle) {
		p, ok := transport.TaskCancellationFromContext(ctx)
		require.True(t, ok)
		requireSameStopProof(t, proof, p)
		conn.result = substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationConfirmed, ErrorClass: substrate.ExecErrorCanceled}
	}
	cold := *d
	cold.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = conn
		return target, err
	}
	// Simulate a Start whose authorization check was already successful before
	// revocation. The durable stop record still prevents provider.Start.
	late, err := cold.Start(context.Background(), ref, req)
	require.NoError(t, err)
	require.True(t, late.CancelRequested)
	require.Zero(t, base.starts)
	require.NoError(t, cold.Recover(context.Background()))
	stopped, err := cold.load(pending.Handle)
	require.NoError(t, err)
	require.True(t, stopped.Reported)
	require.NotEmpty(t, stopped.ProviderNodeID)
}

func TestStopDiscoveryContinuesAfterOneTaskFailure(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	next := g
	next.Epoch++
	next.ActionID = "second"
	next.CommandID = "second"
	other, err := controlplane.SignExecutionCancellation(issuer, next)
	require.NoError(t, err)
	d.PendingStops = func(context.Context, controlplane.GuestTaskStopCursor) (controlplane.GuestTaskStopPage, error) {
		return controlplane.GuestTaskStopPage{ThroughEpoch: next.Epoch, Stops: []controlplane.GuestTaskStop{{Topology: "lab", Node: "broken", Cancellation: proof}, {Topology: "lab", Node: "web", Cancellation: other}}}, nil
	}
	resolve := d.Resolve
	conn := &signedStopConnection{taskConnection: base}
	conn.onStop = func(context.Context, substrate.ExecutionHandle) {
		conn.result = substrate.ExecResult{RemoteTermination: substrate.RemoteTerminationConfirmed, ErrorClass: substrate.ExecErrorCanceled}
	}
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		if ref.Node == "broken" {
			return Target{}, errors.New("broken provider")
		}
		target, err := resolve(ctx, ref)
		target.Connection = conn
		return target, err
	}
	reports := 0
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports++; return nil }
	require.ErrorContains(t, d.Recover(context.Background()), "broken provider")
	require.Equal(t, 1, reports)
	require.Zero(t, base.starts)
}

func (c *signedStopConnection) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	c.onStop(ctx, h)
	return c.result.RemoteTermination, nil
}

func TestDiscoveryStopsGuestWithoutHostRecordOrStartingTask(t *testing.T) {
	d, req, base, issuer := taskFixtureWithIssuer(t)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(issuer, g)
	require.NoError(t, err)
	ref := testRef(req)
	conn := &signedStopConnection{taskConnection: base}
	conn.onStop = func(ctx context.Context, h substrate.ExecutionHandle) {
		received, ok := transport.TaskCancellationFromContext(ctx)
		require.True(t, ok)
		requireSameStopProof(t, proof, received)
		require.Equal(t, g.Epoch, h.Epoch)
		conn.result = substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationConfirmed, ErrorClass: substrate.ExecErrorCanceled}
	}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		target, err := resolve(ctx, ref)
		target.Connection = conn
		return target, err
	}
	d.PendingStops = func(context.Context, controlplane.GuestTaskStopCursor) (controlplane.GuestTaskStopPage, error) {
		return controlplane.GuestTaskStopPage{Stops: []controlplane.GuestTaskStop{{Topology: ref.Topology, Node: ref.Node, Cancellation: proof}}, ThroughEpoch: g.Epoch}, nil
	}
	reports := 0
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		reports++
		require.Equal(t, "cancelled", r.GuestResult.ResultClass)
		return nil
	}
	require.NoError(t, d.Recover(context.Background()))
	require.Zero(t, base.starts)
	require.Equal(t, 1, reports)
	h := substrate.ExecutionHandle{ID: g.ActionID, Digest: g.Digest, Epoch: g.Epoch, BootID: g.TargetBootID, NodeID: ref.Node, TopologyUID: g.Scope.TopologyUID, ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation}
	record, err := d.load(h)
	require.NoError(t, err)
	require.True(t, record.Reported)
	require.NotNil(t, record.Cancellation)
}
