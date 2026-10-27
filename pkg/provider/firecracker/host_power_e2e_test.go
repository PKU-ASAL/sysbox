//go:build e2e

package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type hostPowerTestNode struct {
	*Substrate
	pauseCalls, resumeCalls, connectionCalls *int
}

func (n *hostPowerTestNode) Pause(ctx context.Context, h substrate.NodeHandle) error {
	*n.pauseCalls++
	return n.Substrate.Pause(ctx, h)
}
func (n *hostPowerTestNode) Resume(ctx context.Context, h substrate.NodeHandle) error {
	*n.resumeCalls++
	return n.Substrate.Resume(ctx, h)
}
func (n *hostPowerTestNode) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	*n.connectionCalls++
	return nil, fmt.Errorf("host power must never require a guest connection")
}

func TestFirecrackerHostPowerDispatcherE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	vmRoot := t.TempDir()
	creator := realFirecracker(t, kernel, bin, vmRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	identity := substrate.OperationIdentity{ActionID: "create-power-vm", OwnerUID: "power-topology", TopologyUID: "power-topology", ResourceUID: "power-node", Generation: 2}
	creation := substrate.WithOperationIdentity(ctx, identity)
	handle, err := creator.CreateNode(creation, substrate.NodeSpec{Name: "host-power-e2e", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, creator, handle)
	require.NoError(t, creator.StartNode(creation, handle))
	issuerPublic, issuerPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPublic, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ledger := t.TempDir()
	require.NoError(t, os.Chmod(ledger, 0700))
	scope := controlplane.ExecutionScope{TopologyUID: identity.TopologyUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation}
	ref := hostsupervisor.TargetRef{Topology: "power-lab", Node: "web", Scope: scope}
	pauses, resumes, connections, reports := 0, 0, 0, 0
	newDispatcher := func() *hostsupervisor.PowerDispatcher {
		return &hostsupervisor.PowerDispatcher{
			Root: ledger, AgentID: "owner", HostBootID: "host-boot", IssuerKey: issuerPublic, HostKey: hostPrivate,
			Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil },
			Resolve: func(context.Context, hostsupervisor.TargetRef) (hostsupervisor.NodeTarget, error) {
				// Reconstruct the provider each time: only the persisted manifest
				// and exact handle carry VM identity across dispatcher restarts.
				node := &hostPowerTestNode{Substrate: realFirecracker(t, kernel, bin, vmRoot), pauseCalls: &pauses, resumeCalls: &resumes, connectionCalls: &connections}
				return hostsupervisor.NodeTarget{Scope: scope, AgentID: "owner", Handle: handle, Node: node, Power: node}, nil
			},
			ReportStop: func(_ context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
				require.NoError(t, controlplane.VerifyExecutionStopReceipt(hostPublic, receipt))
				require.Equal(t, "confirmed", receipt.Quiescence)
				require.Equal(t, "node_power", receipt.Grant.Operation)
				require.Equal(t, scope, receipt.Grant.Scope)
				reports++
				return nil
			},
		}
	}
	sign := func(operation string, epoch uint64, target controlplane.ExecutionScope) (controlplane.NodePowerRequest, controlplane.SignedExecutionGrant) {
		request := controlplane.NodePowerRequest{Topology: ref.Topology, Node: ref.Node, Operation: operation}
		digest, err := request.Digest()
		require.NoError(t, err)
		grant := controlplane.ExecutionGrant{TargetBootID: "host-boot", Scope: target, AgentID: "owner", SessionID: "session", ActionID: fmt.Sprintf("%s-%d", operation, epoch), CommandID: fmt.Sprintf("command-%d", epoch), Attempt: 1, Epoch: epoch, Operation: "node_power", Digest: digest, ExpiresAt: time.Now().UTC().Add(time.Minute), State: controlplane.GrantActive}
		signed, err := controlplane.SignExecutionGrant(issuerPrivate, grant, "host-boot", time.Now().UTC())
		require.NoError(t, err)
		return request, signed
	}
	pause, pauseGrant := sign("pause", 1, scope)
	require.NoError(t, newDispatcher().Execute(ctx, ref, pause, pauseGrant))
	observed, err := creator.ObserveNode(creation, handle)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, observed.Status)
	require.NoError(t, newDispatcher().Observe(ctx, ref, pauseGrant))
	resume, resumeGrant := sign("resume", 2, scope)
	require.NoError(t, newDispatcher().Execute(ctx, ref, resume, resumeGrant))
	observed, err = creator.ObserveNode(creation, handle)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusRunning, observed.Status)
	// The older pause completion is historical evidence. Reading it after
	// resume must neither reconnect to the guest nor pause the VM again.
	require.NoError(t, newDispatcher().Observe(ctx, ref, pauseGrant))
	oldScope := scope
	oldScope.Generation--
	oldRef := ref
	oldRef.Scope = oldScope
	stalePause, staleGrant := sign("pause", 3, oldScope)
	require.Error(t, newDispatcher().Execute(ctx, oldRef, stalePause, staleGrant))
	observed, err = creator.ObserveNode(creation, handle)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusRunning, observed.Status)
	require.Equal(t, 1, pauses)
	require.Equal(t, 1, resumes)
	require.Zero(t, connections)
	require.Equal(t, 2, reports)
	t.Log("real VM pause/resume via cold host dispatcher verified; old pause was not replayed, stale generation rejected, no guest connection used")
}
