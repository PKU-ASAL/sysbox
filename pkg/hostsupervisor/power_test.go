package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type powerNode struct {
	driver.Node
	status  substrate.NodeLifecycleStatus
	calls   int
	failure error
}

func (n *powerNode) Pause(context.Context, substrate.NodeHandle) error {
	n.calls++
	n.status = substrate.NodeStatusPaused
	return n.failure
}
func (n *powerNode) Resume(context.Context, substrate.NodeHandle) error {
	n.calls++
	n.status = substrate.NodeStatusRunning
	return n.failure
}
func (n *powerNode) ObserveNode(context.Context, substrate.NodeHandle) (substrate.NodeObservation, error) {
	return substrate.NodeObservation{Exists: true, OwnerUID: "topology", ResourceUID: "node", Generation: 1, Status: n.status}, nil
}
func powerFixture(t *testing.T) (*PowerDispatcher, TargetRef, controlplane.NodePowerRequest, controlplane.SignedExecutionGrant, *powerNode) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := controlplane.NodePowerRequest{Topology: "lab", Node: "web", Operation: "pause"}
	digest, err := req.Digest()
	require.NoError(t, err)
	ref := TargetRef{Topology: req.Topology, Node: req.Node, Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}}
	g := controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "host-boot", AgentID: "owner", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "node_power", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, g, "host-boot", time.Now())
	require.NoError(t, err)
	node := &powerNode{status: substrate.NodeStatusRunning}
	d := &PowerDispatcher{Root: t.TempDir(), AgentID: "owner", HostBootID: "host-boot", IssuerKey: pub, HostKey: host, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ReportStop: func(context.Context, controlplane.SignedExecutionStopReceipt) error { return nil }}
	require.NoError(t, os.Chmod(d.Root, 0700))
	d.Resolve = func(context.Context, TargetRef) (NodeTarget, error) {
		return NodeTarget{Scope: ref.Scope, AgentID: "owner", Handle: substrate.NodeHandle{ID: "actual", Generation: 1}, Node: node, Power: node}, nil
	}
	return d, ref, req, signed, node
}
func TestPowerLostStopReplyRecoversWithoutRepeatingEffect(t *testing.T) {
	d, ref, req, g, node := powerFixture(t)
	var receipts []controlplane.SignedExecutionStopReceipt
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return errors.New("reply lost")
	}
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	require.Equal(t, 1, node.calls)
	cold := *d
	cold.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return nil
	}
	require.NoError(t, cold.Observe(context.Background(), ref, g))
	require.Equal(t, 1, node.calls)
	require.Len(t, receipts, 2)
	require.Equal(t, receipts[0], receipts[1])
}
func TestPowerAmbiguousDaemonResultNeverReplaysOrReportsStop(t *testing.T) {
	d, ref, req, g, node := powerFixture(t)
	node.failure = errors.New("daemon response lost after accepting pause")
	reports := 0
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports++; return nil }
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	// Current desired state alone does not prove a daemon request is finished.
	require.Equal(t, substrate.NodeStatusPaused, node.status)
	require.Error(t, d.Observe(context.Background(), ref, g))
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	require.NoError(t, d.Recover(context.Background()))
	require.Equal(t, 1, node.calls)
	require.Zero(t, reports)
}
func TestPowerRejectsAlteredRequestAndResourceBeforeEffects(t *testing.T) {
	d, ref, req, g, node := powerFixture(t)
	req.Operation = "resume"
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	req.Operation = "pause"
	ref.Scope.Generation++
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	require.Zero(t, node.calls)
}

func TestPowerRechecksAuthorityAfterResolvingTarget(t *testing.T) {
	d, ref, req, g, node := powerFixture(t)
	resolve := d.Resolve
	revoked := false
	d.Resolve = func(ctx context.Context, ref TargetRef) (NodeTarget, error) { revoked = true; return resolve(ctx, ref) }
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		if revoked {
			return errors.New("session revoked during provider lookup")
		}
		return nil
	}
	require.Error(t, d.Execute(context.Background(), ref, req, g))
	require.Zero(t, node.calls)
}
