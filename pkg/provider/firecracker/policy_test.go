package firecracker

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"

	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestPolicyRejectsUnfencedLegacyAdapter(t *testing.T) {
	s := &Substrate{}
	_, err := s.ApplyRuleset(context.Background(), driver.PolicyTarget{}, driver.RulesetSpec{})
	require.Error(t, err)
	_, err = s.ObserveRuleset(context.Background(), driver.PolicyTarget{}, "owner")
	require.Error(t, err)
	require.Error(t, s.DeleteRuleset(context.Background(), driver.PolicyTarget{}, "owner"))
}

// A host Run returns logical node handles; only the provider-local query
// handle is translated. This does not mutate the durable host child record.
func TestPolicyRunHandleUsesCanonicalVMIdentity(t *testing.T) {
	conn := transport.NewVsockConnection("/unused", 8901)
	conn.BindExecutionIdentity("vm-external-id", 2)
	c := &policyGuestConnection{VsockConnection: conn, nodeID: "vm-external-id", generation: 2}
	expected := substrate.ExecutionHandle{ID: "task", NodeID: "logical-web", Generation: 2, Digest: "digest", BootID: "boot", Epoch: 3, TopologyUID: "top", ResourceUID: "node"}
	ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "policy", OwnerUID: "owner", TopologyUID: "top", ResourceUID: "node", Generation: 2})
	ctx = transport.WithTaskStartHandler(ctx, func(context.Context, string, substrate.ExecRequest, substrate.OperationIdentity) (substrate.ExecutionHandle, error) {
		return expected, nil
	})
	got, err := c.Start(ctx, substrate.ExecRequest{Program: "sysbox-policy.v1", Shell: substrate.ShellNone})
	require.NoError(t, err)
	expected.NodeID = "vm-external-id"
	require.Equal(t, expected, got)
}

type policyTaskProbe struct {
	actions []string
	boot    string
}

func (p *policyTaskProbe) Start(ctx context.Context, r substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	p.actions = append(p.actions, id.ActionID)
	return substrate.ExecutionHandle{}, nil
}
func (*policyTaskProbe) ObserveExecution(context.Context, substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return substrate.ExecResult{ExitCodeKnown: true, RemoteTermination: substrate.RemoteTerminationConfirmed}, nil
}
func TestPolicyApplyDeleteUseDistinctDurableTaskIdentity(t *testing.T) {
	node := substrate.OperationIdentity{ActionID: "create-node", OwnerUID: "owner", TopologyUID: "top", ResourceUID: "node", Generation: 1}
	policy := node
	policy.ResourceUID = "policy"
	r := network.GuestPolicyRequest{Node: node, Identity: policy, Operation: "apply", Owner: "lab/fw", Spec: driver.RulesetSpec{Owner: "lab/fw", Family: driver.FamilyIPv4}}
	probe := &policyTaskProbe{}
	require.NoError(t, executeGuestPolicy(context.Background(), probe, r))
	r.Operation = "delete"
	require.NoError(t, executeGuestPolicy(context.Background(), probe, r))
	require.NotEqual(t, probe.actions[0], probe.actions[1])
}

func (p *policyTaskProbe) GuestTaskBootID(context.Context) (string, error) {
	if p.boot == "" {
		return "boot-1", nil
	}
	return p.boot, nil
}
func TestPolicyTaskIdentityChangesAcrossGuestBoot(t *testing.T) {
	node := substrate.OperationIdentity{ActionID: "node", OwnerUID: "owner", TopologyUID: "top", ResourceUID: "node", Generation: 1}
	policy := node
	policy.ResourceUID = "policy"
	r := network.GuestPolicyRequest{Node: node, Identity: policy, Operation: "apply", Owner: "lab/fw", Spec: driver.RulesetSpec{Owner: "lab/fw", Family: driver.FamilyIPv4}}
	probe := &policyTaskProbe{boot: "first"}
	require.NoError(t, executeGuestPolicy(context.Background(), probe, r))
	require.NoError(t, executeGuestPolicy(context.Background(), probe, r))
	require.Equal(t, probe.actions[0], probe.actions[1])
	probe.boot = "second"
	require.NoError(t, executeGuestPolicy(context.Background(), probe, r))
	require.NotEqual(t, probe.actions[0], probe.actions[2])
}

func TestPolicyRecoveryAttemptGetsNewTaskButSameAttemptDeduplicates(t *testing.T) {
	node := substrate.OperationIdentity{ActionID: "node", OwnerUID: "owner", TopologyUID: "top", ResourceUID: "node", Generation: 1}
	policy := node
	policy.ResourceUID = "policy"
	r := network.GuestPolicyRequest{Node: node, Identity: policy, Operation: "delete", Owner: "lab/fw"}
	p := &policyTaskProbe{}
	g := controlplane.SignedExecutionGrant{Grant: controlplane.ExecutionGrant{CommandID: "run-first", Attempt: 1, Epoch: 1}}
	ctx := transport.WithParentExecutionGrant(context.Background(), g)
	require.NoError(t, executeGuestPolicy(ctx, p, r))
	require.NoError(t, executeGuestPolicy(ctx, p, r))
	require.Equal(t, p.actions[0], p.actions[1])
	g.Grant.CommandID = "run-recovery"
	g.Grant.Epoch++
	require.NoError(t, executeGuestPolicy(transport.WithParentExecutionGrant(context.Background(), g), p, r))
	require.NotEqual(t, p.actions[0], p.actions[2])
}
