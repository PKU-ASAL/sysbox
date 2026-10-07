package runtime_test

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestCommitAdoptionEvidenceReplayIsIdempotent(t *testing.T) {
	r, err := persistence.OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	ctx := context.Background()
	st, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	identity := substrate.OperationIdentity{ActionID: "adopt", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "resource", Generation: 1}
	evidence := substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "sha256:fingerprint"}
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), ResourceType: "sysbox_node", Driver: "docker", ExternalID: "external", OwnerUID: st.TopologyUID, ResourceUID: "resource", Generation: 1, Private: []byte(`{"evidence":true}`), Status: state.ResourcePresent}
	require.NoError(t, runtimeCommitEvidence(ctx, r, identity, evidence, resource, st.Meta.Serial))
	after, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	resource.CreatedAt = time.Now().UTC()
	resource.UpdatedAt = resource.CreatedAt
	require.NoError(t, runtimeCommitEvidence(ctx, r, identity, evidence, resource, st.Meta.Serial))
	final, err := r.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, after.Meta.Serial, final.Meta.Serial)
	evidence.Fingerprint = "different-proof"
	require.Error(t, runtimeCommitEvidence(ctx, r, identity, evidence, resource, final.Meta.Serial))
}

func TestAdoptionEvidenceCannotUseExistingResourceAsCompletionProof(t *testing.T) {
	repo, err := persistence.OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	mgr := state.NewManagerWithBackend(repo.StateBackend("lab"))
	st, err := mgr.Load()
	require.NoError(t, err)
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "external", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
	st.Resources = []state.Resource{resource}
	require.NoError(t, mgr.Save(st))
	id := substrate.OperationIdentity{ActionID: "unrecorded", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: resource.ResourceUID, Generation: 1}
	proof := substrate.AdoptionEvidence{Provider: "docker", ExternalID: resource.ExternalID, Fingerprint: "fingerprint"}
	require.ErrorIs(t, runtime.CommitAdoptionEvidence(ctx, repo, "run", "lab", resource.Address, proof, resource, id, st.Meta.Serial), runtime.ErrPlanStale)
	actions, err := repo.LoadActions(ctx, "lab")
	require.NoError(t, err)
	require.Empty(t, actions)
}
func runtimeCommitEvidence(ctx context.Context, r *persistence.Repository, id substrate.OperationIdentity, e substrate.AdoptionEvidence, res state.Resource, serial int64) error {
	return runtime.CommitAdoptionEvidence(ctx, r, "adopt", "lab", res.Address, e, res, id, serial)
}

type interruptedAdoptionRepository struct {
	runtime.ActionRepository
	committed bool
}

func (r interruptedAdoptionRepository) CommitActionOutcome(ctx context.Context, action runtime.ActionRecord, patch *runtime.StatePatch) error {
	if r.committed {
		if err := r.ActionRepository.CommitActionOutcome(ctx, action, patch); err != nil {
			return err
		}
	}
	return errors.New("injected loss at outcome boundary")
}

func TestAdoptionEvidenceRecoversDurableIntentAndLostOutcome(t *testing.T) {
	for _, backend := range []string{"file", "sqlite"} {
		for _, committed := range []bool{false, true} {
			t.Run(backend+map[bool]string{false: "/intent", true: "/outcome"}[committed], func(t *testing.T) {
				root := t.TempDir()
				open := func() *persistence.Repository {
					var repo *persistence.Repository
					var err error
					if backend == "file" {
						repo, err = persistence.OpenFilesystem(root)
					} else {
						repo, err = persistence.OpenSQLite(filepath.Join(root, "state.db"))
					}
					require.NoError(t, err)
					return repo
				}
				repo := open()
				ctx := context.Background()
				st, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				id := substrate.OperationIdentity{ActionID: "adopt", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "resource", Generation: 1}
				proof := substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "fingerprint"}
				resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "external", OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: 1, Private: []byte(`{"anchor":"original"}`), CreatedAt: time.Now().UTC()}
				firstCreated := resource.CreatedAt
				err = runtime.CommitAdoptionEvidence(ctx, interruptedAdoptionRepository{repo, committed}, "run", "lab", resource.Address, proof, resource, id, st.Meta.Serial)
				require.ErrorContains(t, err, "injected loss")
				require.NoError(t, repo.Close())
				repo = open()
				defer repo.Close()
				resource.CreatedAt = time.Now().UTC()
				require.NoError(t, runtime.CommitAdoptionEvidence(ctx, repo, "run", "lab", resource.Address, proof, resource, id, st.Meta.Serial))
				after, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				require.Equal(t, st.Meta.Serial+1, after.Meta.Serial)
				require.Len(t, after.Resources, 1)
				require.True(t, firstCreated.Equal(after.Resources[0].CreatedAt))
				actions, err := repo.LoadActions(ctx, "lab")
				require.NoError(t, err)
				require.Len(t, actions, 1)
				require.Equal(t, runtime.ActionSucceeded, actions[0].Status)
				resource.Private = []byte(`{"anchor":"different"}`)
				require.Error(t, runtime.CommitAdoptionEvidence(ctx, repo, "run", "lab", resource.Address, proof, resource, id, st.Meta.Serial))
			})
		}
	}
}
