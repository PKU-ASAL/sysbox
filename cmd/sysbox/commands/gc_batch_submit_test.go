package commands

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGCBatchSubmitRequiresCompleteFreshPlanAndOneTopology(t *testing.T) {
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "id", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "uid", Generation: 1, InventoryDigest: "digest"}
	plan := gcBatchPlan{Version: 1, Complete: true, Scope: "local-filesystem-and-host", CreatedAt: time.Now(), Topologies: map[string]string{"owner": "lab"}, Candidates: []controlplane.GCCandidate{c}}
	path := filepath.Join(t.TempDir(), "plan.json")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/topologies/lab/gc/batch", r.URL.Path)
		require.Equal(t, "batch-one", r.Header.Get("Idempotency-Key"))
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"reservations":[]}`))
	}))
	defer server.Close()
	run := func() error {
		raw, err := json.Marshal(plan)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
		cmd := newGCBatchSubmitCommand()
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetArgs([]string{path, "--api", server.URL, "--topology", "lab", "--key", "batch-one"})
		return cmd.Execute()
	}
	require.NoError(t, run())
	require.Equal(t, 1, calls)
	plan.Complete = false
	require.Error(t, run())
	plan.Complete = true
	plan.CreatedAt = time.Now().Add(-time.Hour)
	require.Error(t, run())
	require.Equal(t, 1, calls)
}
