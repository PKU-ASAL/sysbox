package guesttask

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func policyTaskFixture(t *testing.T) Request {
	id := substrate.OperationIdentity{ActionID: "action", OwnerUID: "owner", TopologyUID: "top", ResourceUID: "node", Generation: 2}
	policy := id
	policy.ResourceUID = "fw"
	raw, err := json.Marshal(network.GuestPolicyRequest{Operation: "apply", Node: id, Identity: policy, Owner: "lab/fw", Spec: driver.RulesetSpec{Owner: "lab/fw", Family: driver.FamilyIPv4, DefaultInput: driver.VerdictDrop, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop}})
	require.NoError(t, err)
	return Request{ID: "policy", UserID: 65534, Args: []string{"sysbox-policy.v1"}, Input: raw, OutputLimit: 4096, Grant: SignedGrant{Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "top", ResourceUID: "node", Generation: 2}}}}
}
func TestPolicyBuiltinRejectsUnboundPrivilege(t *testing.T) {
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Args = append(r.Args, "/bin/sh") },
		func(r *Request) { r.Environment = map[string]string{"LD_PRELOAD": "evil"} },
		func(r *Request) { r.WorkingDir = "/tmp" },
		func(r *Request) { r.Grant.Grant.Scope.Generation++ },
		func(r *Request) { r.Grant.Grant.Scope.ResourceUID = "other" },
	} {
		r := policyTaskFixture(t)
		mutate(&r)
		_, err := PolicyBuiltin(r)
		require.Error(t, err)
	}
}
func TestPolicyBuiltinBindsTypedRequestAndDigest(t *testing.T) {
	r := policyTaskFixture(t)
	p, err := PolicyBuiltin(r)
	require.NoError(t, err)
	require.Equal(t, "apply", p.Operation)
	digest, err := r.Digest()
	require.NoError(t, err)
	r.Input = append(r.Input, ' ')
	changed, err := r.Digest()
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
}

func TestPolicyBuiltinUnsignedCannotReachPrivilegedEffects(t *testing.T) {
	req := policyTaskFixture(t)
	p, err := PolicyBuiltin(req)
	require.NoError(t, err)
	_, err = ExecutePolicyBuiltin(context.Background(), req, nil, "boot", p.Node, t.TempDir())
	require.ErrorIs(t, err, ErrUnauthorized)
}
