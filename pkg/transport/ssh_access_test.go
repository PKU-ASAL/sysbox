package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type sshAccessCaller func(guesttask.Call) guesttask.Response

func (f sshAccessCaller) taskCall(_ context.Context, call guesttask.Call) (guesttask.Response, error) {
	return f(call), nil
}

func TestSSHAccessTransportBindsMutationAndResponse(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	public, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	m := substrate.SSHAccessMutation{ID: "apply", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))}}}
	digest, err := m.Digest()
	require.NoError(t, err)
	g := controlplane.SignedExecutionGrant{BootID: "boot", Grant: controlplane.ExecutionGrant{
		Scope:    controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 3},
		ActionID: m.ID, Epoch: 2, Digest: digest, Operation: "guest_ssh_access_apply", TargetBootID: "boot",
	}}
	ctx := WithTaskGrant(context.Background(), m.ID, g)
	identity := executionIdentity{nodeID: "node", generation: 3}
	h := substrate.SSHAccessHandle{ID: m.ID, Digest: digest, User: "ubuntu", AccessID: "admin", Revision: 1,
		BootID: "boot", Epoch: 2, TopologyUID: "owner", ResourceUID: "resource", Generation: 3}
	for _, field := range []string{"exact", "scope", "epoch", "revision", "digest", "node", "presence"} {
		t.Run(field, func(t *testing.T) {
			caller := sshAccessCaller(func(call guesttask.Call) guesttask.Response {
				require.Equal(t, "ssh-access", call.Operation)
				require.Equal(t, &m, call.SSHMutation)
				r := substrate.SSHAccessResult{Handle: h, Present: true}
				switch field {
				case "scope":
					r.Handle.ResourceUID = "other"
				case "epoch":
					r.Handle.Epoch++
				case "revision":
					r.Handle.Revision++
				case "digest":
					r.Handle.Digest = "other"
				case "node":
					r.Handle.NodeID = "other"
				case "presence":
					r.Present = false
				}
				return guesttask.Response{Version: guesttask.Version, SSHResult: &r}
			})
			result, err := mutateSSHAccess(ctx, caller, identity, m)
			if field == "exact" {
				require.NoError(t, err)
				require.Equal(t, "node", result.Handle.NodeID)
			} else {
				require.Error(t, err)
				trusted := h
				trusted.NodeID = identity.nodeID
				require.Equal(t, trusted, result.Handle, "recovery uses expected identity, never an untrusted response")
				require.False(t, result.Present, "an invalid response must not report success")
			}
		})
	}
	calls := 0
	caller := sshAccessCaller(func(guesttask.Call) guesttask.Response { calls++; return guesttask.Response{} })
	rejected, err := mutateSSHAccess(context.Background(), caller, identity, m)
	require.Error(t, err)
	require.Empty(t, rejected.Handle.ID, "pre-dispatch rejection must not mint an operation handle")
	identity.generation++
	_, err = mutateSSHAccess(ctx, caller, identity, m)
	require.Error(t, err)
	require.Zero(t, calls)
}

func TestSSHAccessTransportSeparatesHistoricalResultAndCurrentInspection(t *testing.T) {
	h := substrate.SSHAccessHandle{NodeID: "node", ID: "apply", Digest: "digest", User: "ubuntu", AccessID: "admin", Revision: 1,
		BootID: "boot", Epoch: 2, TopologyUID: "owner", ResourceUID: "resource", Generation: 3}
	identity := executionIdentity{nodeID: "node", generation: 3}
	caller := sshAccessCaller(func(call guesttask.Call) guesttask.Response {
		require.Empty(t, call.SSHHandle.NodeID)
		if call.Operation == "ssh-observe" {
			return guesttask.Response{Version: guesttask.Version, SSHResult: &substrate.SSHAccessResult{Handle: *call.SSHHandle, Present: true}}
		}
		return guesttask.Response{Version: guesttask.Version, SSHHandle: call.SSHHandle, SSHInspection: &substrate.SSHAccessInspection{Status: "absent", Revision: 2}}
	})
	result, err := observeSSHAccess(context.Background(), caller, identity, h)
	require.NoError(t, err)
	require.True(t, result.Present)
	current, err := inspectSSHAccess(context.Background(), caller, identity, h)
	require.NoError(t, err)
	require.Equal(t, "absent", current.Status)
}

func TestSSHStopTransportRequiresExactScopeAndCessation(t *testing.T) {
	req := substrate.SSHAccessMutation{ID: "stop", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	g := controlplane.SignedExecutionGrant{BootID: "guest", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}, Epoch: 2, TargetBootID: "guest", Operation: "guest_ssh_access_delete", ActionID: req.ID, Digest: digest}}
	for _, variant := range []string{"exact", "scope", "epoch", "not-stopped", "success"} {
		t.Run(variant, func(t *testing.T) {
			caller := sshAccessCaller(func(call guesttask.Call) guesttask.Response {
				require.Equal(t, "ssh-stop", call.Operation)
				require.Equal(t, &g, call.Grant)
				h := substrate.SSHAccessHandle{ID: req.ID, Digest: digest, User: "ubuntu", AccessID: "admin", Revision: 2, BootID: "guest", Epoch: 2, TopologyUID: "owner", ResourceUID: "node", Generation: 1}
				stop := substrate.SSHAccessStop{Handle: h, Quiescent: true, Outcome: "unknown"}
				switch variant {
				case "scope":
					stop.Handle.ResourceUID = "other"
				case "epoch":
					stop.Handle.Epoch++
				case "not-stopped":
					stop.Quiescent = false
				case "success":
					stop.Outcome = "committed"
				}
				return guesttask.Response{Version: guesttask.Version, SSHStop: &stop}
			})
			stop, err := stopSSHAccess(WithTaskGrant(context.Background(), req.ID, g), caller, executionIdentity{nodeID: "provider", generation: 1}, req)
			if variant != "exact" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "provider", stop.Handle.NodeID)
		})
	}
}
