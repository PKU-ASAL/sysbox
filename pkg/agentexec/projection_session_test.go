package agentexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestReportResourceProjectionStampsCurrentAgentSessionForFirecracker(t *testing.T) {
	var got controlplane.ResourceProjection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/agents/host-a/projections/resources", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	opts := Options{
		APIURL: server.URL, ID: "host-a",
		Labels: map[string]string{controlplane.AgentSessionLabel: "current-process"},
	}
	require.NoError(t, opts.ReportResourceProjection(context.Background(), controlplane.ResourceProjection{
		Topology: "lab", SessionID: "previous-process",
		Resources: []controlplane.ResourceHealth{{Provider: "firecracker"}},
	}))
	require.Equal(t, "host-a", got.AgentID)
	require.Equal(t, "current-process", got.SessionID)
}

func TestReportResourceProjectionKeepsDockerSessionPayload(t *testing.T) {
	var got controlplane.ResourceProjection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	opts := Options{
		APIURL: server.URL, ID: "host-a",
		Labels: map[string]string{controlplane.AgentSessionLabel: "current-process"},
	}
	require.NoError(t, opts.ReportResourceProjection(context.Background(), controlplane.ResourceProjection{
		Topology: "lab", SessionID: "previous-process",
		Resources: []controlplane.ResourceHealth{{Provider: "docker"}},
	}))
	require.Equal(t, "previous-process", got.SessionID)
}
