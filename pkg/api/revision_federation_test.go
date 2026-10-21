package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestSQLFederatedRetirementRejectsLateRunAndLostConsumer(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		if _, file := store.(*localAPIStore); file {
			return
		}
		ctx := context.Background()
		consumer := stateDeleteTestServer(t, store)
		repo, err := consumer.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		version, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, version, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
		token := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(token, []byte("federation-test-secret"), 0600))
		consumer.cfg.RevisionUpstreams = map[string]string{"central": token}
		httpConsumer := httptest.NewServer(consumer)
		defer httpConsumer.Close()
		central := NewServer(t.TempDir(), t.TempDir())
		central.cfg.RevisionRegistryID = "central"
		central.cfg.RevisionSQLConsumers = map[string]config.RevisionSQLConsumer{"sql": {URL: httpConsumer.URL, TokenFile: token}}
		files := map[string][]byte{"field.sysbox.hcl": []byte("federated")}
		id := controlplane.ComputeRevisionDigest(files)
		rev := controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}
		require.NoError(t, central.apiStore.SaveGlobalRevision(ctx, rev))
		require.NoError(t, store.SaveGlobalRevision(ctx, rev))
		require.NoError(t, central.deleteLocalRevisionBatch(ctx, []string{id}, RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1}))
		_, err = store.GetGlobalRevision(ctx, id)
		require.Error(t, err)
		require.Error(t, store.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
		files = map[string][]byte{"field.sysbox.hcl": []byte("another")}
		next := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, central.apiStore.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: next, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
		central.cfg.RevisionSQLConsumers = nil
		require.Error(t, central.deleteLocalRevisionBatch(ctx, []string{next}, RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1}), "dropping config cannot forget consumer")
		_, err = central.apiStore.GetGlobalRevision(ctx, next)
		require.NoError(t, err)
	})
}

func TestSQLFederationLostAcknowledgementResumesWithoutPrematureCentralDelete(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		if _, file := store.(*localAPIStore); file {
			return
		}
		ctx := context.Background()
		consumer := stateDeleteTestServer(t, store)
		token := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(token, []byte("federation-test-secret"), 0600))
		consumer.cfg.RevisionUpstreams = map[string]string{"central": token}
		repo, err := consumer.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
		var lose atomic.Bool
		lose.Store(true)
		httpConsumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response := httptest.NewRecorder()
			consumer.ServeHTTP(response, r)
			if response.Code == 200 && lose.Swap(false) {
				http.Error(w, "lost durable ack", 502)
				return
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
		}))
		defer httpConsumer.Close()
		central := NewServer(t.TempDir(), t.TempDir())
		central.cfg.RevisionRegistryID = "central"
		central.cfg.RevisionSQLConsumers = map[string]config.RevisionSQLConsumer{"sql": {URL: httpConsumer.URL, TokenFile: token}}
		files := map[string][]byte{"field.sysbox.hcl": []byte("lost reply")}
		id := controlplane.ComputeRevisionDigest(files)
		rev := controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}
		require.NoError(t, central.apiStore.SaveGlobalRevision(ctx, rev))
		require.NoError(t, store.SaveGlobalRevision(ctx, rev))
		policy := RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 1}
		require.Error(t, central.deleteLocalRevisionBatch(ctx, []string{id}, policy))
		_, err = central.apiStore.GetGlobalRevision(ctx, id)
		require.NoError(t, err, "unacknowledged consumer cannot authorize central deletion")
		_, err = store.GetGlobalRevision(ctx, id)
		require.Error(t, err, "consumer durable retirement survives lost ack")
		require.NoError(t, central.deleteLocalRevisionBatch(ctx, []string{id}, policy))
		_, err = central.apiStore.GetGlobalRevision(ctx, id)
		require.Error(t, err)
	})
}

func TestSQLFederationCorruptMembershipAndRawBypassRejected(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	root := filepath.Join(s.runsDir, "global-revisions")
	require.NoError(t, os.Mkdir(root, 0700))
	s.cfg.RevisionRegistryID = "central"
	s.cfg.RevisionSQLConsumers = map[string]config.RevisionSQLConsumer{"sql": {URL: "https://example.invalid", TokenFile: "/unused"}}
	_, err := s.enrolledSQLConsumers(root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sql-consumers.json"), nil, 0600))
	s.cfg.RevisionSQLConsumers = nil
	_, err = s.enrolledSQLConsumers(root)
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, ".sql-consumers.json")))
	files := map[string][]byte{"field.sysbox.hcl": []byte("protected")}
	id := controlplane.ComputeRevisionDigest(files)
	require.NoError(t, s.apiStore.SaveGlobalRevision(context.Background(), controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
	require.Error(t, s.apiStore.(*localAPIStore).DeleteGlobalRevisionAtomic(context.Background(), id, time.Now()))
}

func TestSQLFederationRetainsEntireCentralBatchOnConsumerConflict(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		if _, file := store.(*localAPIStore); file {
			return
		}
		ctx := context.Background()
		consumer := stateDeleteTestServer(t, store)
		token := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(token, []byte("federation-test-secret"), 0600))
		consumer.cfg.RevisionUpstreams = map[string]string{"central": token}
		repo, err := consumer.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
		httpConsumer := httptest.NewServer(consumer)
		defer httpConsumer.Close()
		central := NewServer(t.TempDir(), t.TempDir())
		central.cfg.RevisionRegistryID = "central"
		central.cfg.RevisionSQLConsumers = map[string]config.RevisionSQLConsumer{"sql": {URL: httpConsumer.URL, TokenFile: token}}
		ids := []string{}
		for _, value := range []string{"first", "second"} {
			files := map[string][]byte{"field.sysbox.hcl": []byte(value)}
			id := controlplane.ComputeRevisionDigest(files)
			ids = append(ids, id)
			rev := controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}
			require.NoError(t, central.apiStore.SaveGlobalRevision(ctx, rev))
			require.NoError(t, store.SaveGlobalRevision(ctx, rev))
		}
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "held", Topology: "lab", Revision: ids[1], Status: controlplane.RunDone, QueuedAt: time.Now(), EndedAt: time.Now()}))
		require.Error(t, central.deleteLocalRevisionBatch(ctx, ids, RevisionRetentionPolicy{OlderThan: time.Now(), Limit: 2}))
		for _, id := range ids {
			_, err = central.apiStore.GetGlobalRevision(ctx, id)
			require.NoError(t, err)
			_, err = store.GetGlobalRevision(ctx, id)
			require.NoError(t, err, "consumer transaction must roll back whole batch")
		}
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "first-still-admissible", Topology: "lab", Revision: ids[0], Status: controlplane.RunQueued, QueuedAt: time.Now()}))
	})
}
