package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestHostFenceResetSSHCommittedDispositionAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, true, false, false, false, true)
}
func TestHostFenceResetSSHLostCommittedDispositionAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, true, false, false, false, true, true)
}
func TestHostFenceResetSSHUnknownDispositionAPI(t *testing.T) {
	runHostFenceAPIContract(t, "", true, true, true, false, true, false, false, true, true, false, true)
}
func seedResetSSHAPI(t *testing.T, repo *persistence.Repository, action *runtime.ActionRecord) runtime.ResetSSHDependency {
	t.Helper()
	ctx := context.Background()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	cfg := substrate.SSHAccessConfig{User: "ubuntu", AccessID: "login", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}}
	addr := address.Resource("sysbox_ssh_access", "login")
	depID := fmt.Sprintf("%x", sha256.Sum256([]byte("reset-ssh\x00"+action.ID+"\x00"+addr.String())))
	resource := func(id, node string, resourceGeneration, nodeGeneration int64) state.Resource {
		digest, err := (substrate.SSHAccessMutation{ID: id, Config: cfg}).Digest()
		require.NoError(t, err)
		return state.Resource{Address: addr, Driver: "managed", SchemaVersion: 2, OwnerUID: action.OwnerUID, ResourceUID: "ssh-resource", Generation: resourceGeneration, ExternalID: cfg.AccessID, Attributes: state.MustAttributes(map[string]any{"ssh_access": map[string]any{"version": 1, "node": action.Resource.String(), "config": cfg, "handle": substrate.SSHAccessHandle{NodeID: node, ID: id, Digest: digest, User: cfg.User, AccessID: cfg.AccessID, Revision: 1, BootID: "guest", TopologyUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: nodeGeneration, Epoch: 1}}})}
	}
	previous := resource("original-ssh", action.Candidate.ExternalID, 1, 1)
	result := resource(depID, "replacement", 2, 2)
	st, err := repo.LoadState(ctx, action.Topology)
	require.NoError(t, err)
	seed := runtime.ActionRecord{ID: "ssh-seed", Topology: action.Topology, RunID: "older-run", Resource: addr, Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: action.OwnerUID, ResourceUID: previous.ResourceUID, Generation: 1, StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial, Candidate: &previous}
	require.NoError(t, repo.CommitActionIntent(ctx, seed))
	records, err := repo.LoadActions(ctx, action.Topology)
	require.NoError(t, err)
	for _, record := range records {
		if record.ID == seed.ID {
			seed = record
		}
	}
	seed.Status = runtime.ActionSucceeded
	require.NoError(t, repo.CommitActionOutcome(ctx, seed, &runtime.StatePatch{Resource: addr.String(), Action: seed.Action, Op: runtime.StatePatchUpsert}))
	st, err = repo.LoadState(ctx, action.Topology)
	require.NoError(t, err)
	action.StateSerial = st.Meta.Serial
	action.ExpectedSerial = st.Meta.Serial
	previous = *st.FindResource(addr)
	return runtime.ResetSSHDependency{Previous: previous, DesiredDigest: "desired", ActionID: depID, Config: cfg, Result: &result}
}
