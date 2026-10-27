package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestResetSSHAtomicPublicationAndMonotonicEvidence(t *testing.T) {
	for _, backend := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			r := resetTestRepository(t, backend)
			ctx := context.Background()
			mgr := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := mgr.Load()
			require.NoError(t, err)
			node := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExternalID: "old-node"}
			makeSSH := func(actionID, nodeID string, nodeGeneration, generation int64) state.Resource {
				config := substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin", AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBVJklJOs11mczPrSlMNBIqfguwuvQcCjdOgRqihdz5L"}}
				mutation := substrate.SSHAccessMutation{ID: actionID, Config: config}
				digest, err := mutation.Digest()
				require.NoError(t, err)
				handle := substrate.SSHAccessHandle{ID: actionID, Digest: digest, NodeID: nodeID, TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: nodeGeneration, User: "ubuntu", AccessID: "admin", Revision: 1, BootID: "guest", Epoch: 1}
				return state.Resource{Address: address.Resource("sysbox_ssh_access", "admin"), OwnerUID: st.TopologyUID, ResourceUID: "ssh", Generation: generation, ExternalID: "admin", SchemaVersion: 2, Attributes: state.MustAttributes(map[string]any{"ssh_access": map[string]any{"version": 1, "node": node.Address.String(), "handle": handle, "config": config}})}
			}
			old := makeSSH("old-action", node.ExternalID, 1, 1)
			st.AddResource(node)
			st.AddResource(old)
			require.NoError(t, mgr.Save(st))
			st, err = mgr.Load()
			require.NoError(t, err)
			old = *st.FindResource(old.Address)
			node = *st.FindResource(node.Address)
			sum := sha256.Sum256([]byte("reset-ssh\x00reset\x00" + old.Address.String()))
			a := runtime.ActionRecord{ID: "reset", RunID: "run", Topology: "lab", Resource: node.Address, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 2, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &node, Action: controlplane.PlanActionReset, Status: runtime.ActionIntentCommitted, Phase: "accepted", ResetSSH: []runtime.ResetSSHDependency{{Previous: old, ActionID: hex.EncodeToString(sum[:]), DesiredDigest: "config"}}}
			var saved struct {
				Config substrate.SSHAccessConfig `json:"config"`
			}
			raw, err := json.Marshal(old.Attributes["ssh_access"])
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &saved))
			a.ResetSSH[0].Config = saved.Config
			omitted := a
			omitted.ResetSSH = nil
			require.Error(t, r.CommitResetIntents(ctx, []runtime.ActionRecord{omitted}))
			require.NoError(t, r.CommitResetIntents(ctx, []runtime.ActionRecord{a}))
			a.Revision = 1
			for _, phase := range []string{"preparing", "prepared", "destroying", "destroy_returned", "destroyed", "applying", "verifying"} {
				a.Phase, a.Status = phase, runtime.ActionUncertain
				if phase == "verifying" {
					replacement := node
					replacement.Generation, replacement.ExternalID = 2, "new-node"
					a.Candidate, a.EffectComplete = &replacement, true
				}
				require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
				a.Revision++
			}
			patch := &runtime.StatePatch{Resource: node.Address.String(), Action: a.Action, Op: runtime.StatePatchUpsert}
			missing := a
			missing.Status = runtime.ActionSucceeded
			require.Error(t, r.CommitActionOutcome(ctx, missing, patch))
			a.ResetSSH[0].Started = true
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
			a.Revision++
			result := makeSSH(a.ResetSSH[0].ActionID, "new-node", 2, 2)
			a.ResetSSH[0].Result = &result
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
			a.Revision++
			before, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.EqualValues(t, 1, before.FindResource(node.Address).Generation)
			require.EqualValues(t, 1, before.FindResource(old.Address).Generation)
			for _, change := range []string{"discard", "unstart", "snapshot", "result"} {
				raw, err := json.Marshal(a)
				require.NoError(t, err)
				var bad runtime.ActionRecord
				require.NoError(t, json.Unmarshal(raw, &bad))
				switch change {
				case "discard":
					bad.ResetSSH[0].Result = nil
				case "unstart":
					bad.ResetSSH[0].Started = false
				case "snapshot":
					bad.ResetSSH[0].Previous.Generation++
				case "result":
					bad.ResetSSH[0].Result.ExternalID = "foreign"
				}
				require.Error(t, r.CommitActionOutcome(ctx, bad, nil), change)
			}
			a.Status = runtime.ActionSucceeded
			require.NoError(t, r.CommitActionOutcome(ctx, a, patch))
			require.NoError(t, r.CommitActionOutcome(ctx, a, patch), "lost commit acknowledgment is idempotent")
			after, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.EqualValues(t, 2, after.FindResource(node.Address).Generation)
			require.EqualValues(t, 2, after.FindResource(old.Address).Generation)
			require.Equal(t, before.Meta.Serial+1, after.Meta.Serial)
		})
	}
}
