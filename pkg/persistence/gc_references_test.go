package persistence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestGCReferencesRetainJournalAndSnapshotSources(t *testing.T) {
	for _, kind := range []string{"file", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var repo *Repository
			var err error
			switch kind {
			case "file":
				repo, err = OpenFilesystem(t.TempDir())
			case "sqlite":
				repo, err = OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
			case "postgres":
				dsn := os.Getenv("SYSBOX_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("SYSBOX_TEST_POSTGRES_DSN not set")
				}
				repo, err = OpenPostgres(ctx, dsn)
			}
			require.NoError(t, err)
			defer repo.Close()
			topology := "gc_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			require.NoError(t, repo.update(ctx, topology, func(a *aggregate) error {
				r := state.Resource{Address: address.Resource("sysbox_node", "vm"), Driver: "firecracker", ExternalID: "vm", OwnerUID: a.State.TopologyUID, ResourceUID: "vm-uid", Generation: 1}
				a.Serial = 1
				a.Actions["create"] = runtime.ActionRecord{ID: "create", Revision: 1, Topology: topology, RunID: "run", Resource: r.Address, Action: controlplane.PlanActionCreate, Status: runtime.ActionSucceeded, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: 1, Candidate: &r}
				return nil
			}))
			first, err := repo.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			require.Len(t, first.References, 1, "journal-only candidate remains referenced")
			require.Equal(t, "action/create", first.References[0].Source)
			require.Equal(t, "vm", first.References[0].ExternalID)
			again, err := repo.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			require.Equal(t, first, again, "stable source produces stable digest")
			backend := repo.StateBackend(topology).(state.SnapshotBackend)
			snap, err := backend.Snapshot(ctx, "retain historical journal")
			require.NoError(t, err)
			require.NoError(t, repo.update(ctx, topology, func(a *aggregate) error { a.Actions = map[string]runtime.ActionRecord{}; return nil }))
			retained, err := repo.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			require.Len(t, retained.References, 1)
			require.Equal(t, "snapshot/"+snap.ID+"/action/create", retained.References[0].Source)
			require.NotEqual(t, first.Digest, retained.Digest)
			require.NoError(t, repo.StateBackend(topology).(state.SnapshotDeleter).DeleteSnapshot(ctx, snap.ID))
			empty, err := repo.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			require.Empty(t, empty.References)
			require.NotEqual(t, retained.Digest, empty.Digest)
		})
	}
}

func TestGCReferencesUseOneTransactionAndRejectUnresolvedJournal(t *testing.T) {
	before, after := newAggregate(), newAggregate()
	before.Serial = 1
	before.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "firecracker", ExternalID: "vm", OwnerUID: before.State.TopologyUID, ResourceUID: "vm", Generation: 1}}
	b := &completingSnapshotBackend{before: before, after: after}
	repo := &Repository{backend: b}
	snapshot, err := repo.LoadGCReferences(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, 1, b.reads)
	require.Len(t, snapshot.References, 1)
	snapshot.References[0].ExternalID = "changed"
	require.Equal(t, "vm", before.State.Resources[0].ExternalID)
	after.Actions["pending"] = runtime.ActionRecord{ID: "pending", Revision: 1, Topology: "lab", RunID: "run", OwnerUID: after.State.TopologyUID, ResourceUID: "pending", Resource: address.Resource("sysbox_node", "pending"), Generation: 1, Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted}
	snapshot, err = repo.LoadGCReferences(context.Background(), "lab")
	require.ErrorIs(t, err, ErrConflict)
	require.Empty(t, snapshot.Digest)
	require.Empty(t, snapshot.References)
}

func TestGCReferencesRejectInvalidRetainedEvidenceWithoutPartialResult(t *testing.T) {
	for _, scenario := range []string{"corrupt", "unresolved"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo, err := OpenFilesystem(t.TempDir())
			require.NoError(t, err)
			defer repo.Close()
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
				a.Serial = 1
				a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "vm"), Driver: "firecracker", ExternalID: "vm", OwnerUID: a.State.TopologyUID, ResourceUID: "vm", Generation: 1}}
				if scenario == "unresolved" {
					a.Actions["pending"] = runtime.ActionRecord{ID: "pending", Revision: 1, Topology: "lab", RunID: "run", OwnerUID: a.State.TopologyUID, ResourceUID: "pending", Resource: address.Resource("sysbox_node", "pending"), Generation: 1, Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted}
				}
				return nil
			}))
			snapshot, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, scenario)
			require.NoError(t, err)
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
				a.Actions = map[string]runtime.ActionRecord{}
				if scenario == "corrupt" {
					snap := a.Snapshots[snapshot.ID]
					snap.Data = []byte(`{`)
					a.Snapshots[snapshot.ID] = snap
				}
				return nil
			}))
			out, err := repo.LoadGCReferences(ctx, "lab")
			if scenario == "corrupt" {
				require.ErrorIs(t, err, ErrCorruptRecord)
			} else {
				require.ErrorIs(t, err, ErrConflict)
			}
			require.Equal(t, GCReferenceSnapshot{}, out, "current state must not escape when retained evidence is invalid")
		})
	}
}
