package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestFileInstallDigestExcludesDeliveryAndStopRequiresQuiescence(t *testing.T) {
	cmd := AgentCommand{ID: "command", AgentID: "agent", Type: "guest_file_put", ActionID: "install", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, FilePut: &GuestFilePut{ID: "install", Topology: "lab", Node: "node", Path: "/opt/data/file", Size: 3, Mode: 0640, SHA256: strings.Repeat("a", 64), FetchRef: "/old"}}
	request, err := FromFileInstallCommand(cmd)
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	cmd.FilePut.FetchRef = "https://worker-selected.invalid/"
	next, err := FromFileInstallCommand(cmd)
	require.NoError(t, err)
	after, err := next.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, after)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "install", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "guest_file_install", Digest: digest, TargetBootID: "guest", State: GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	result := substrate.FileInstallResult{Outcome: substrate.FileInstallCommitted, Quiescent: true, SHA256: request.SHA256, Size: request.Size, Mode: request.Mode}
	receipt, err := SignExecutionFileStopReceipt(key, grant, "host", "guest", request.ID, digest, result, "receipt", time.Now())
	require.NoError(t, err)
	require.NoError(t, VerifyExecutionStopReceipt(pub, receipt))
	for _, r := range []substrate.FileInstallResult{{Outcome: substrate.FileInstallCommitted}, {Outcome: substrate.FileInstallUnknown, Quiescent: true}} {
		_, err = SignExecutionFileStopReceipt(key, grant, "host", "guest", request.ID, digest, r, "receipt", time.Now())
		require.Error(t, err)
	}
	_, err = SignExecutionFileStopReceipt(key, grant, "host", "other-guest", request.ID, digest, result, "receipt", time.Now())
	require.Error(t, err)
}
