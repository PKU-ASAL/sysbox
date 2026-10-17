package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestAdoptionInspectionPersistsSignedReadOnlyEvidence(t *testing.T) {
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := controlplane.AdoptionInspectionRequest{ID: "inspect", Topology: "lab", TopologyUID: "owner", Address: "sysbox_node.web", Provider: "docker", AgentID: "agent", ExternalID: "external", ResourceUID: "resource", Generation: 1}
	digest, err := req.Digest()
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "adoption_inspect", Digest: digest, TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "boot", time.Now())
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	reports := 0
	c := &AdoptionCoordinator{Root: root, IssuerKey: pub, HostKey: key, AgentID: "agent", HostBootID: "boot", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		reports++
		return controlplane.VerifyAdoptionStopReceipt(hostPub, r, raw)
	}}
	calls := 0
	_, err = c.Inspect(context.Background(), req, signed, func(context.Context) (substrate.AdoptionEvidence, error) {
		calls++
		return substrate.AdoptionEvidence{Provider: "docker", ExternalID: "exact-id", Fingerprint: "sha256:fingerprint", State: json.RawMessage(`{}`)}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, reports)
	require.NoError(t, c.Recover(context.Background()))
	require.Equal(t, 1, calls)
}
