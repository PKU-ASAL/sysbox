package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func adoptionFixture(t *testing.T) (*AdoptionCoordinator, AdoptionRequest, controlplane.SignedExecutionGrant) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	req := AdoptionRequest{Topology: "lab", TopologyUID: "topology", Address: "sysbox_node.web", Provider: "docker", ExternalID: "external", ResourceUID: "resource", Generation: 1, ExpectedSerial: 4, Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "sha256:evidence"}}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}, AgentID: "agent", SessionID: "session", ActionID: "adopt", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: d, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return &AdoptionCoordinator{Root: root, IssuerKey: pub, HostBootID: "host", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, signed
}
func TestAdoptionApplyLostReplyObservesWithoutReapplying(t *testing.T) {
	c, req, g := adoptionFixture(t)
	calls := 0
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("reply lost") })
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
	_, err = c.Observe(context.Background(), req, g)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
}
func TestAdoptionRejectsClientEvidenceMismatch(t *testing.T) {
	c, req, g := adoptionFixture(t)
	req.Evidence.Fingerprint = "forged"
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.Error(t, err)
	_ = c
}
