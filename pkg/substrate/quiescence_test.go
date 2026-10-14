package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuiescenceProofBindsOperationAndEvidence(t *testing.T) {
	id := OperationIdentity{ActionID: "action", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 2}
	evidence := map[string]any{"daemon_revision": "r1", "stopped": true}
	raw := []byte(`{"daemon_revision":"r1","stopped":true}`)
	digest := sha256.Sum256(raw)
	p := QuiescenceProof{OperationID: "op-1", Anchor: "daemon:r1", Identity: id, Evidence: evidence, Digest: "sha256:" + hex.EncodeToString(digest[:])}
	require.NoError(t, p.Validate(id, "op-1"))
	require.Error(t, p.Validate(id, "op-2"))
	p.Evidence["stopped"] = false
	require.Error(t, p.Validate(id, "op-1"))
}

type unsupportedQuiescer struct{}

func (unsupportedQuiescer) ProveQuiescence(context.Context, NodeHandle, string, OperationIdentity) (QuiescenceProof, error) {
	return QuiescenceProof{}, ErrQuiescenceUnsupported
}

func TestQuiescenceSPICanFailClosed(t *testing.T) {
	_, err := unsupportedQuiescer{}.ProveQuiescence(context.Background(), NodeHandle{}, "op", OperationIdentity{})
	require.ErrorIs(t, err, ErrQuiescenceUnsupported)
}
