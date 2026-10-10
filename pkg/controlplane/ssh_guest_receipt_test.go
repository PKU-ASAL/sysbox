package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestSSHStopRequiresBoundCommittedResult(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, deleting := range []bool{false, true} {
		req := substrate.SSHAccessMutation{ID: "change", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{sshAccessTestKey(t)}}}
		operation := "guest_ssh_access_apply"
		if deleting {
			req.Delete, req.ExpectedRevision, req.Config.AuthorizedKeys = true, 2, nil
			operation = "guest_ssh_access_delete"
		}
		digest, err := req.Digest()
		require.NoError(t, err)
		g := ExecutionGrant{Scope: ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 2}, TargetBootID: "guest", AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 4, Operation: operation, Digest: digest, State: GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
		result := substrate.SSHAccessResult{Present: !deleting, Handle: substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: "web", User: "ubuntu", AccessID: "admin", Revision: req.ExpectedRevision + 1, BootID: "guest", TopologyUID: "owner", ResourceUID: "node", Generation: 2, Epoch: 4}}
		_, err = SignExecutionStopReceipt(key, g, "host", "generic", time.Now())
		require.Error(t, err, "generic quiescence cannot prove an SSH configuration result")
		receipt, err := SignGuestSSHAccessStopReceipt(key, g, "host", req, result, "receipt", time.Now())
		require.NoError(t, err)
		require.NoError(t, VerifyExecutionStopReceipt(pub, receipt))
		require.NoError(t, VerifyGuestSSHAccessStopReceipt(pub, receipt, req, "web"))
		require.Error(t, VerifyGuestSSHAccessStopReceipt(pub, receipt, req, "other-node"))
		require.Error(t, VerifyGuestSSHAccessStopReceipt(pub, receipt, req, ""))
		wrongRevision := receipt
		wrongResult := *receipt.SSHResult
		wrongResult.Handle.Revision++
		wrongRevision.SSHResult = &wrongResult
		wrongRevision, err = signStopReceipt(key, wrongRevision)
		require.NoError(t, err, "generic signature validation does not know canonical CAS revision")
		require.NoError(t, VerifyExecutionStopReceipt(pub, wrongRevision))
		require.Error(t, VerifyGuestSSHAccessStopReceipt(pub, wrongRevision, req, "web"), "a valid host signature cannot bless the wrong canonical revision")
		for _, field := range []string{"scope", "guest", "epoch", "revision", "user", "access", "digest", "presence"} {
			wrong := result
			switch field {
			case "scope":
				wrong.Handle.ResourceUID = "other"
			case "guest":
				wrong.Handle.BootID = "other"
			case "epoch":
				wrong.Handle.Epoch++
			case "revision":
				wrong.Handle.Revision++
			case "user":
				wrong.Handle.User = "root"
			case "access":
				wrong.Handle.AccessID = "other"
			case "digest":
				wrong.Handle.Digest = "other"
			case "presence":
				wrong.Present = !wrong.Present
			}
			_, err := SignGuestSSHAccessStopReceipt(key, g, "host", req, wrong, "invalid", time.Now())
			require.Error(t, err, field)
		}
		altered := req
		altered.ExpectedRevision++
		require.Error(t, VerifyGuestSSHAccessStopReceipt(pub, receipt, altered, "web"))
	}
}
