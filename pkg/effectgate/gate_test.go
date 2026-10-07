package effectgate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGatePersistsOutcomeAndRejectsOldEpoch(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "create", Digest: "spec-digest", ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(private, grant, "boot", time.Now())
	require.NoError(t, err)
	gate := Gate{Root: t.TempDir(), PublicKey: public, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	calls := 0
	effect := func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"external_id":"one"}`), nil
	}
	first, err := gate.Execute(context.Background(), signed, "effect", effect)
	require.NoError(t, err)
	cold := gate
	again, err := cold.Execute(context.Background(), signed, "effect", effect)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(again))
	require.Equal(t, 1, calls)
	grant.Epoch = 2
	grant.ActionID = "new-action"
	next, err := controlplane.SignExecutionGrant(private, grant, "boot", time.Now())
	require.NoError(t, err)
	_, err = cold.Execute(context.Background(), next, "next-effect", effect)
	require.NoError(t, err)
	_, err = cold.Execute(context.Background(), signed, "old-effect", effect)
	require.Error(t, err)
	require.Equal(t, 2, calls)
}

func TestGateUnknownOutcomeCannotBeReplayedOrRetired(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "destroy", Digest: "digest", ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(private, grant, "boot", time.Now())
	require.NoError(t, err)
	gate := Gate{Root: t.TempDir(), PublicKey: public, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	calls := 0
	_, err = gate.Execute(context.Background(), signed, "effect", func(context.Context) (json.RawMessage, error) { calls++; return nil, context.DeadlineExceeded })
	require.Error(t, err)
	_, err = gate.Execute(context.Background(), signed, "effect", func(context.Context) (json.RawMessage, error) { calls++; return nil, nil })
	require.Error(t, err)
	require.Equal(t, 1, calls)
	_, err = gate.SealStop(context.Background(), grant, "effect", func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		t.Fatal("unknown effect cannot be signed")
		return controlplane.SignedExecutionStopReceipt{}, nil
	})
	require.Error(t, err, "cannot certify unknown in-flight external effect as stopped")
}

func TestGateEpochFloorFollowsOverlappingScope(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	gate := Gate{Root: t.TempDir(), PublicKey: pub, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "a", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "cmd", Attempt: 1, Epoch: 2, Operation: "update", Digest: "digest", ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	newer, _ := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	effect := func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	_, err := gate.Execute(context.Background(), newer, "a", effect)
	require.NoError(t, err)
	grant.Scope.ResourceUID = "b"
	grant.Epoch = 1
	olderOther, _ := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	_, err = gate.Execute(context.Background(), olderOther, "b", effect)
	require.NoError(t, err, "independent active resource grant must not be fenced by another resource's epoch")
	grant.Scope.ResourceUID = ""
	grant.Scope.Generation = 0
	oldTopology, _ := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	_, err = gate.Execute(context.Background(), oldTopology, "all", effect)
	require.ErrorIs(t, err, ErrFenced)
}
