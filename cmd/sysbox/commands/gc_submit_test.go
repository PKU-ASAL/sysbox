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
)

func TestGCSubmitOnlyDispatchesExactPlanToAPI(t *testing.T) {
	candidate := controlplane.GCCandidate{Kind: "container", ExternalID: "orphan", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "digest"}
	path := filepath.Join(t.TempDir(), "plan.json")
	raw, err := json.Marshal(map[string]any{"topology": "lab", "candidate": candidate})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/topologies/lab/gc", r.URL.Path)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "gc-key", r.Header.Get("Idempotency-Key"))
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		var got controlplane.GCCandidate
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		require.Equal(t, candidate, got)
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"gc-1","agent_id":"owning-host"}`))
	}))
	defer server.Close()
	command := newGCSubmitCommand()
	command.SetArgs([]string{path, "--api", server.URL, "--key", "gc-key", "--token", "token"})
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, command.Execute())
	require.Equal(t, 1, calls)
	require.Contains(t, output.String(), "gc-1")
}
