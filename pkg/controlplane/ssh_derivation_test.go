package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestSSHDerivationHostSignatureBindsParentTargetAndMutation(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	request := SSHGrantDerivation{Parent: SignedExecutionGrant{Grant: ExecutionGrant{AgentID: "agent", Epoch: 1, Digest: "parent"}}, Request: substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}, ResourceUID: "node", Generation: 1, GuestBootID: "guest"}
	signed, err := SignSSHGrantDerivation(key, request)
	require.NoError(t, err)
	require.NoError(t, VerifySSHGrantDerivation(public, signed))
	for _, field := range []string{"parent", "agent", "epoch", "resource", "generation", "boot", "user", "access", "revision", "action", "signature"} {
		t.Run(field, func(t *testing.T) {
			changed := signed
			changed.HostSignature = append([]byte(nil), signed.HostSignature...)
			switch field {
			case "parent":
				changed.Parent.Grant.Digest = "other"
			case "agent":
				changed.Parent.Grant.AgentID = "other"
			case "epoch":
				changed.Parent.Grant.Epoch++
			case "resource":
				changed.ResourceUID = "other"
			case "generation":
				changed.Generation++
			case "boot":
				changed.GuestBootID = "other"
			case "user":
				changed.Request.Config.User = "root"
			case "access":
				changed.Request.Config.AccessID = "other"
			case "revision":
				changed.Request.ExpectedRevision++
			case "action":
				changed.Request.ID = "other"
			case "signature":
				changed.HostSignature[0] ^= 1
			}
			require.Error(t, VerifySSHGrantDerivation(public, changed))
		})
	}
	foreign, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.Error(t, VerifySSHGrantDerivation(foreign, signed))
	require.NoError(t, VerifySSHGrantDerivation(public, signed))
}
