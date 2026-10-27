package agentexec

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLeaseRenewalCannotRewriteExecutingRun(t *testing.T) {
	run := controlplane.Run{ID: "run", Topology: "lab", Revision: "digest", SnapshotPath: "/canonical", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "lease", Attempt: 1, QueuedAt: time.Now().UTC()}
	original := run
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changed := run
		changed.SnapshotPath = "/foreign"
		changed.Status = controlplane.RunDone
		require.NoError(t, json.NewEncoder(w).Encode(changed))
	}))
	defer server.Close()
	err := renewRunLease(context.Background(), Options{APIURL: server.URL, ID: "agent"}, &run)
	require.Error(t, err)
	require.Equal(t, original, run)
}
