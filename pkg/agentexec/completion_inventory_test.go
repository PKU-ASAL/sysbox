package agentexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestCompletionPublishesCurrentInventoryBeforeTerminalRun(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	saveLocalTestState(t, statePath, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address: address.Resource("sysbox_node", "vm"),
			Driver:  "firecracker", ExternalID: "vm-1",
		}},
	})
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "field.hcl"), StatePath: statePath, RunsDir: root})
	mgr, err := bridge.StateManager("lab")
	require.NoError(t, err)
	st, err := mgr.Load()
	require.NoError(t, err)
	var requests []string
	failProjection := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/projections/resources") {
			var projection controlplane.ResourceProjection
			require.NoError(t, json.NewDecoder(r.Body).Decode(&projection))
			require.Equal(t, st.Meta.Serial, projection.Serial)
			require.Len(t, projection.Resources, 1)
			require.Equal(t, "firecracker", projection.Resources[0].Provider)
			require.False(t, projection.ObservedAt.After(time.Now()))
			if failProjection {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	b := remoteBridge{Bridge: bridge, reporter: Options{APIURL: server.URL, ID: "host-a", ExecutionSessionID: "process", observationSequence: &atomic.Uint64{}}}
	run := &controlplane.Run{ID: "run-a", Topology: "lab", Status: controlplane.RunDone}
	require.Error(t, b.ReportRunComplete(context.Background(), run, controlplane.Projection{}))
	require.Len(t, requests, 1)
	failProjection = false
	require.NoError(t, b.ReportRunComplete(context.Background(), run, controlplane.Projection{}))
	require.Equal(t, []string{
		"/v1/agents/host-a/projections/resources",
		"/v1/agents/host-a/projections/resources",
		"/v1/agents/host-a/runs/run-a/complete",
	}, requests)
}

func TestDockerCompletionKeepsArchiveReportingPath(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	saveLocalTestState(t, statePath, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address: address.Resource("sysbox_node", "web"), Driver: "docker",
		}},
	})
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: filepath.Join(root, "field.hcl"), StatePath: statePath, RunsDir: root})
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	b := remoteBridge{Bridge: bridge, reporter: Options{APIURL: server.URL, ID: "host-a", ExecutionSessionID: "process", observationSequence: &atomic.Uint64{}}}
	run := &controlplane.Run{ID: "run-docker", Topology: "lab", Status: controlplane.RunDone}
	require.NoError(t, b.ReportRunComplete(context.Background(), run, controlplane.Projection{}))
	require.Equal(t, []string{"/v1/agents/host-a/runs/run-docker/complete"}, requests)
}
