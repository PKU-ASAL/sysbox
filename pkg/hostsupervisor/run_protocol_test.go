package hostsupervisor

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRunRequestDigestBindsRevisionActionAndGeneration(t *testing.T) {
	req := RunRequest{RunID: "run", Topology: "lab", Revision: "sha256:rev", ActionID: "action", ResourceUID: "resource", Generation: 2, Action: "apply", ExpectedSerial: 3}
	req.TopologyUID = "lab-uid"
	digest, err := req.Digest()
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "lab", ResourceUID: "resource", Generation: 2}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: digest, State: controlplane.GrantActive, TargetBootID: "host", ExpiresAt: time.Now().Add(time.Minute)}
	g.Scope.TopologyUID = req.TopologyUID
	signed, err := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, err)
	require.NoError(t, ValidateRunGrant(signed, req, "host"))
	req.Revision = "sha256:other"
	require.Error(t, ValidateRunGrant(signed, req, "host"))
	req.Revision = "sha256:rev"
	req.Generation = 3
	require.Error(t, ValidateRunGrant(signed, req, "host"))
}

func TestRunRequestRejectsUnsupportedActionAndPaths(t *testing.T) {
	req := RunRequest{RunID: "run", Topology: "lab", Revision: "rev", ActionID: "a", ResourceUID: "r", Generation: 1, Action: "reset"}
	req.TopologyUID = "uid"
	require.Error(t, req.Validate())
	req.Action = "apply"
	req.RunID = "../run"
	require.Error(t, req.Validate())
}
