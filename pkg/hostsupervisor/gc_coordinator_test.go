package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func gcFixture(t *testing.T) (*GCCoordinator, GCCandidate, controlplane.SignedExecutionGrant) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	candidate := GCCandidate{Kind: "container", ExternalID: "external", TopologyUID: "topology", OwnerUID: "topology", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
	d, e := candidate.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "host", SessionID: "session", ActionID: "gc", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: d, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return &GCCoordinator{Root: root, IssuerKey: pub, HostBootID: "host", AgentID: "host", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ValidateInventory: func(context.Context, GCCandidate) error { return nil }}, candidate, signed
}

func TestGCDeleteRequiresFreshInventoryRevalidation(t *testing.T) {
	c, candidate, g := gcFixture(t)
	called := 0
	c.ValidateInventory = func(context.Context, GCCandidate) error { called++; return errors.New("inventory changed") }
	_, err := c.Delete(context.Background(), candidate, g, func(context.Context) (json.RawMessage, error) { t.Fatal("effect must not run"); return nil, nil })
	require.ErrorContains(t, err, "inventory changed")
	require.Equal(t, 1, called)
}
func TestGCDeleteLostReplyObservesWithoutReplay(t *testing.T) {
	c, candidate, g := gcFixture(t)
	calls := 0
	effect := func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("reply lost") }
	_, err := c.Delete(context.Background(), candidate, g, effect)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
	raw, err := c.Observe(context.Background(), candidate, g)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Nil(t, raw)
	_, err = c.Delete(context.Background(), candidate, g, func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"deleted":true}`), nil
	})
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
}
func TestGCCandidateBindsInventoryIdentity(t *testing.T) {
	c, candidate, g := gcFixture(t)
	candidate.InventoryDigest = "new"
	_, err := c.Delete(context.Background(), candidate, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.Error(t, err)
	_ = c
}
