package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGCSourcePublicationFencesDeletionAcrossReopen(t *testing.T) {
	for _, kind := range []string{"file", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			topology := "gcsrc_" + uuid.NewString()
			path := filepath.Join(t.TempDir(), "store")
			open := func() *Repository {
				var r *Repository
				var err error
				if kind == "file" {
					r, err = OpenFilesystem(path)
				} else if kind == "sqlite" {
					r, err = OpenSQLite(path)
				} else {
					dsn := os.Getenv("SYSBOX_TEST_POSTGRES_DSN")
					if dsn == "" {
						t.Skip("SYSBOX_TEST_POSTGRES_DSN required")
					}
					r, err = OpenPostgres(ctx, dsn)
				}
				require.NoError(t, err)
				return r
			}
			r := open()
			defer r.Close()
			initial, err := r.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			version, err := r.BeginGCSource(ctx, topology, "remote", initial.TopologyUID, 0)
			require.NoError(t, err)
			_, err = r.LoadGCReferences(ctx, topology)
			require.ErrorIs(t, err, ErrConflict)
			other := open()
			defer other.Close()
			manifest := GCSourceManifest{Domains: map[string][]string{"revision": {"rev"}, "artifact": {}, "cache": {}, "placement": {"agent"}}, ValidUntil: time.Now().Add(time.Hour)}
			require.NoError(t, other.CompleteGCSource(ctx, topology, "remote", initial.TopologyUID, version, manifest))
			refs, err := r.LoadGCReferences(ctx, topology)
			require.NoError(t, err)
			require.NotEqual(t, initial.Digest, refs.Digest)
			candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: initial.Digest}
			require.ErrorIs(t, r.ReserveGC(ctx, topology, GCReservation{ID: "stale", AgentID: "agent", Candidate: candidate}), ErrConflict)
			candidate.InventoryDigest = refs.Digest
			require.NoError(t, r.ReserveGC(ctx, topology, GCReservation{ID: "delete", AgentID: "agent", Candidate: candidate}))
			_, err = other.BeginGCSource(ctx, topology, "remote", refs.TopologyUID, version+1)
			require.ErrorIs(t, err, ErrConflict)
			_, err = other.BeginGCSource(ctx, topology, "new-source", refs.TopologyUID, 0)
			require.ErrorIs(t, err, ErrConflict)
			cmd := controlplane.AgentCommand{ID: "delete", ActionID: "delete", AgentID: "agent", Type: "gc_delete", TopologyUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, GCKind: "container", GCExternalID: "orphan", GCInventoryDigest: refs.Digest}
			require.NoError(t, other.ValidateCommandLifecycle(ctx, topology, cmd))
			raceTopology := topology + "-race"
			raceRefs, err := r.LoadGCReferences(ctx, raceTopology)
			require.NoError(t, err)
			raceVersion, err := r.BeginGCSource(ctx, raceTopology, "remote", raceRefs.TopologyUID, 0)
			require.NoError(t, err)
			require.NoError(t, r.CompleteGCSource(ctx, raceTopology, "remote", raceRefs.TopologyUID, raceVersion, manifest))
			raceRefs, err = r.LoadGCReferences(ctx, raceTopology)
			require.NoError(t, err)
			raceCandidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: raceRefs.TopologyUID, OwnerUID: raceRefs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: raceRefs.Digest}
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				results <- r.ReserveGC(ctx, raceTopology, GCReservation{ID: "delete", AgentID: "agent", Candidate: raceCandidate})
			}()
			go func() {
				<-start
				_, err := other.BeginGCSource(ctx, raceTopology, "remote", raceRefs.TopologyUID, 2)
				results <- err
			}()
			close(start)
			first, second := <-results, <-results
			if first == nil {
				require.ErrorIs(t, second, ErrConflict)
			} else {
				require.ErrorIs(t, first, ErrConflict)
				require.NoError(t, second)
			}

		})
	}
}

func TestGCSourceRejectsIncompleteExpiredAndReferencedDeletion(t *testing.T) {
	ctx := context.Background()
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	initial, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "remote", initial.TopologyUID, 0)
	require.NoError(t, err)
	m := GCSourceManifest{ValidUntil: time.Now().Add(time.Hour)}
	require.Error(t, r.CompleteGCSource(ctx, "lab", "remote", initial.TopologyUID, v, m))
	m.Domains = map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}
	m.References = []GCResourceReference{{Source: "cache/object", ResourceType: "sysbox_node", Driver: "docker", ExternalID: "used", OwnerUID: initial.TopologyUID, ResourceUID: "used-uid", Generation: 1}}
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "remote", initial.TopologyUID, v, m))
	refs, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	require.Len(t, refs.References, 1)
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "used", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "other-uid", Generation: 1, InventoryDigest: refs.Digest}
	require.ErrorIs(t, r.ReserveGC(ctx, "lab", GCReservation{ID: "delete", AgentID: "agent", Candidate: c}), ErrConflict)
	require.NoError(t, r.update(ctx, "lab", func(a *aggregate) error {
		s := a.GCSources["remote"]
		s.Manifest.ValidUntil = time.Now().Add(-time.Minute)
		a.GCSources["remote"] = s
		return nil
	}))
	_, err = r.LoadGCReferences(ctx, "lab")
	require.ErrorIs(t, err, ErrConflict)
}

func TestGCSourceEnrollmentRemainsVisibleAndCannotBeErased(t *testing.T) {
	ctx := context.Background()
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	refs, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	_, err = r.BeginGCSource(ctx, "lab", "remote", refs.TopologyUID, 0)
	require.NoError(t, err)
	entries, err := r.ListTopologies(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.ErrorIs(t, r.StateBackend("lab").(*stateBackend).Delete(ctx), ErrConflict)
}

func TestGCSourceReservationPinsFreshnessUntilReceipt(t *testing.T) {
	a := newAggregate()
	a.Format = FormatVersion
	now := time.Now()
	a.GCSources = map[string]GCSource{"remote": {Version: 2, Manifest: GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: now.Add(time.Minute)}}}
	first, err := gcReferencesAt(a, now)
	require.NoError(t, err)
	_, err = gcReferencesAt(a, now.Add(time.Hour))
	require.ErrorIs(t, err, ErrConflict)
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: first.TopologyUID, OwnerUID: first.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: first.Digest}
	require.NoError(t, reserveGC(a, GCReservation{ID: "delete", AgentID: "agent", Candidate: candidate}))
	pinned, err := gcReferencesAt(a, now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, first.Digest, pinned.Digest)
}

func TestGCSourceRejectsCorruptInitialManifest(t *testing.T) {
	for _, s := range []GCSource{{Version: 3, Updating: true}, {Version: 1, Updating: true, Manifest: GCSourceManifest{References: []GCResourceReference{{Source: "hidden"}}}}} {
		a := newAggregate()
		a.Format = FormatVersion
		a.GCSources = map[string]GCSource{"remote": s}
		require.ErrorIs(t, validateAggregate(a, "lab"), ErrCorruptRecord)
	}
}

func TestGCSourceSnapshotRetainsReferencesAfterSourceUpdate(t *testing.T) {
	ctx := context.Background()
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	refs, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(ctx, "lab", "remote", refs.TopologyUID, 0)
	require.NoError(t, err)
	m := GCSourceManifest{Domains: map[string][]string{"revision": {"retained"}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour), References: []GCResourceReference{{Source: "revision/vm", ResourceType: "sysbox_node", Driver: "libvirt", ExternalID: "domain", OwnerUID: refs.TopologyUID, ResourceUID: "vm", Generation: 1}}}
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "remote", refs.TopologyUID, v, m))
	b := r.StateBackend("lab").(*stateBackend)
	snap, err := b.Snapshot(ctx, "retain")
	require.NoError(t, err)
	v, err = r.BeginGCSource(ctx, "lab", "remote", refs.TopologyUID, 2)
	require.NoError(t, err)
	m.References = nil
	m.Domains["revision"] = nil
	require.NoError(t, r.CompleteGCSource(ctx, "lab", "remote", refs.TopologyUID, v, m))
	retained, err := r.LoadGCReferences(ctx, "lab")
	require.NoError(t, err)
	require.Len(t, retained.References, 1)
	require.Contains(t, retained.References[0].Source, "snapshot/"+snap.ID)
	require.Equal(t, 1, retained.Domains["revision"].Count)
	require.NoError(t, b.RestoreSnapshot(ctx, snap.ID))
	sources, err := r.LoadGCSources(ctx, "lab")
	require.NoError(t, err)
	require.EqualValues(t, 4, sources["remote"].Version)
	require.Empty(t, sources["remote"].Manifest.References)
}
