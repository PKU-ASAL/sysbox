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
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "topology", Revision: "sha256:rev", ActionID: "action", ResourceUID: "resource", Generation: 2, Action: "apply", Target: "sysbox_node.web", ExpectedSerial: 3}
	req.TopologyUID = "lab-uid"
	digest, err := req.Digest()
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "lab"}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: digest, State: controlplane.GrantActive, TargetBootID: "host", ExpiresAt: time.Now().Add(time.Minute)}
	g.Scope.TopologyUID = req.TopologyUID
	signed, err := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, err)
	require.NoError(t, ValidateRunGrant(signed, req, "host"))
	oldScope := signed
	oldScope.Grant.Scope.ResourceUID, oldScope.Grant.Scope.Generation = req.ResourceUID, req.Generation
	require.Error(t, ValidateRunGrant(oldScope, req, "host"), "old resource-scoped parent grants are not accepted")
	req.Revision = "sha256:other"
	require.Error(t, ValidateRunGrant(signed, req, "host"))
	req.Revision = "sha256:rev"
	req.Generation = 3
	require.Error(t, ValidateRunGrant(signed, req, "host"))
}

func TestApplyTargetBeforeFirstCreation(t *testing.T) {
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "owner", Revision: "rev", ActionID: "action", Action: "apply", Target: "sysbox_node.web"}
	require.NoError(t, req.Validate())
	digest, err := req.Digest()
	require.NoError(t, err)
	req.Target = "sysbox_node.other"
	changed, err := req.Digest()
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
	req.Action = "reset"
	require.Error(t, req.Validate(), "reset requires an existing resource identity")
}

func TestRunChildTargetRestriction(t *testing.T) {
	run := RunRequest{Topology: "lab", TopologyUID: "owner", Target: "sysbox_node.web", ResourceUID: "old", Generation: 1}
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "replacement", Generation: 2}}
	require.NoError(t, run.ValidateChildTarget(ref), "a canonical replacement can receive a child grant")
	for _, field := range []string{"topology", "owner", "node", "resource", "generation", "resource-type"} {
		t.Run(field, func(t *testing.T) {
			r, target := run, ref
			switch field {
			case "topology":
				target.Topology = "other"
			case "owner":
				target.Scope.TopologyUID = "other"
			case "node":
				target.Node = "other"
			case "resource":
				target.Scope.ResourceUID = ""
			case "generation":
				target.Scope.Generation = 0
			case "resource-type":
				r.Target = "sysbox_ssh_access.web"
			}
			require.Error(t, r.ValidateChildTarget(target))
		})
	}
	run.Target, run.ResourceUID, run.Generation = "", "", 0
	require.NoError(t, run.ValidateChildTarget(ref))
}

func TestRunRequestRejectsUnsupportedActionAndPaths(t *testing.T) {
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "topology", Revision: "rev", ActionID: "a", ResourceUID: "r", Generation: 1, Action: "unsupported", Target: "sysbox_node.web", ExpectedSerial: 1}
	req.TopologyUID = "uid"
	require.Error(t, req.Validate())
	req.Action = "apply"
	req.RunID = "../run"
	require.Error(t, req.Validate())
}

func TestNewTopologyRunAcceptsCanonicalSerialZero(t *testing.T) {
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "uid", Revision: "sha256:revision", ActionID: "action", Action: "apply", ExpectedSerial: 0}
	require.NoError(t, req.Validate())
	req.ExpectedSerial = -1
	require.Error(t, req.Validate())
}
