package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGlobalRevisionDeleteHTTPFencesRunAdmission(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		files := map[string][]byte{"field.sysbox.hcl": []byte("unused")}
		id := controlplane.ComputeRevisionDigest(files)
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now().Add(-time.Hour)}))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "lab")
		require.NoError(t, err)
		v, err := repo.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
		require.NoError(t, err)
		require.NoError(t, repo.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, persistence.GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
		body, _ := json.Marshal(map[string]any{"revision": id, "older_than": time.Now().UTC()})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/revisions/delete", strings.NewReader(string(body))))
		if _, local := store.(*controlstore.Local); local {
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Error(t, store.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}))
			return
		}
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Error(t, store.SaveRun(ctx, controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}), "raw run persistence cannot bypass deletion fence")
		require.Error(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "late-command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusQueued, Run: &controlplane.Run{ID: "late", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}}), "independent command cannot resurrect deleted reference")
		require.NoError(t, store.SaveGlobalRevision(ctx, controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: time.Now()}))
		require.Error(t, store.SaveRun(ctx, controlplane.Run{ID: "republished", Topology: "lab", Revision: id, Status: controlplane.RunQueued, QueuedAt: time.Now()}), "republication cannot revive retired Run reference")
		_, err = store.GetRun(ctx, "late")
		require.Error(t, err)
	})
}
