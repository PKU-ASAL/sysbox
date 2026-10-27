package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilePayloadWrongAgentCannotDeleteAndActiveExpiryIsRetained(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"owner", "other"} {
		require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: id}))
	}
	dir := filepath.Join(s.runsDir, "_guest-files")
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, "install")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0600))
	require.NoError(t, writeLocalObject(path+".json", guestFilePayload{AgentID: "owner", ExpiresAt: time.Now().Add(-time.Hour)}))
	require.NoError(t, s.apiStore.SaveGuestFileOperation(ctx, controlplane.GuestFileOperation{ID: "install", Version: 1, AgentID: "owner", Topology: "lab", Node: "node", Status: controlplane.GuestExecutionRunning}))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/agents/other/guest-files/install", nil))
	require.Equal(t, 404, w.Code)
	_, err := os.Stat(path)
	require.NoError(t, err)
	s.gcGuestFilePayloads(time.Now())
	_, err = os.Stat(path)
	require.NoError(t, err)
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/agents/owner/guest-files/install", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "data", w.Body.String())
}
