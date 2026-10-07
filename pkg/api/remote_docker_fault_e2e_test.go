//go:build e2e

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/stretchr/testify/require"
)

// Runs the production HTTP/WS agent loop and Docker driver. API and agent have
// separate durable SQLite repositories on one host; immutable HCL snapshots
// share the filesystem, so this is not a cross-host transport acceptance test.
func TestRemoteDockerCompletionLossRecoveryE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires real Docker daemon; set SYSBOX_REMOTE_DOCKER_E2E=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	docker, err := dockerprovider.New()
	require.NoError(t, err)
	defer docker.Close()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer cli.Close()
	_, err = cli.Ping(ctx)
	require.NoError(t, err)
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "1", Node: docker, NIC: docker, NodeState: docker, Artifact: docker, ImageEntry: docker, GuestExec: docker, GuestNetwork: docker}))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "api.db")
	first := NewServerWithConfig(cfg)
	require.NoError(t, first.initErr)
	agentCfg := cfg
	agentCfg.Paths.RunsDir = t.TempDir()
	agentCfg.Paths.WorkspacesDir = t.TempDir()
	agentCfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "agent.db")
	bridge := NewExecutionBridge(agentCfg)
	topology := "remote-e2e-" + uuid.NewString()[:8]
	repository, err := bridge.ActionRepository(topology)
	require.NoError(t, err)
	initial, err := repository.LoadState(ctx, topology)
	require.NoError(t, err)
	owned := filters.NewArgs(filters.Arg("label", "sysbox.owner_uid="+initial.TopologyUID))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		instances, listErr := cli.ContainerList(cleanup, container.ListOptions{All: true, Filters: owned})
		require.NoError(t, listErr)
		for _, instance := range instances {
			require.Equal(t, initial.TopologyUID, instance.Labels["sysbox.owner_uid"])
			require.NoError(t, cli.ContainerRemove(cleanup, instance.ID, container.RemoveOptions{Force: true}))
		}
	}()
	var current atomic.Pointer[Server]
	current.Store(first)
	lost := make(chan controlplane.RunCompletion, 1)
	acknowledged := make(chan struct{}, 1)
	var drop atomic.Bool
	drop.Store(true)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") && strings.Contains(r.URL.Path, "/runs/") && drop.CompareAndSwap(true, false) {
			raw, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				http.Error(w, "read failed", 500)
				return
			}
			var completion controlplane.RunCompletion
			if json.Unmarshal(raw, &completion) != nil {
				http.Error(w, "decode failed", 500)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			record := httptest.NewRecorder()
			current.Load().ServeHTTP(record, r)
			if record.Code != 200 {
				t.Errorf("completion rejected: %d %s", record.Code, record.Body.String())
			}
			lost <- completion
			conn, _, hijackErr := w.(http.Hijacker).Hijack()
			if hijackErr == nil {
				_ = conn.Close()
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/complete") && strings.Contains(r.URL.Path, "/runs/") {
			record := httptest.NewRecorder()
			current.Load().ServeHTTP(record, r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(record.Code)
			_, _ = w.Write(record.Body.Bytes())
			if record.Code == 200 {
				select {
				case acknowledged <- struct{}{}:
				default:
				}
			}
			return
		}
		current.Load().ServeHTTP(w, r)
	}))
	defer host.Close()
	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	opts := agentexec.Options{ExecutionSessionFile: filepath.Join(t.TempDir(), "session.json"), APIURL: host.URL, ID: "fault-agent", Secret: "test-agent-secret", Capabilities: []string{"docker", "veth"}, PollInterval: 20 * time.Millisecond, Policy: cfg.Agent.Policy}
	go func() { agentDone <- agentexec.Run(agentCtx, opts, bridge) }()
	defer func() {
		stopAgent()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	require.Eventually(t, func() bool { _, err := first.apiStore.GetAgent(ctx, opts.ID); return err == nil }, 5*time.Second, 20*time.Millisecond)
	request := func(method, path string, body any, key string) (int, []byte) {
		raw, marshalErr := json.Marshal(body)
		require.NoError(t, marshalErr)
		req, newErr := http.NewRequestWithContext(ctx, method, host.URL+path, bytes.NewReader(raw))
		require.NoError(t, newErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Sysbox-Roles", "admin")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if strings.HasPrefix(path, "/v1/agents/") {
			require.NoError(t, agent.SignRequest(req, opts.ID, opts.Secret, time.Now()))
		}
		response, callErr := http.DefaultClient.Do(req)
		require.NoError(t, callErr)
		defer response.Body.Close()
		data, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		return response.StatusCode, data
	}
	hcl := fmt.Sprintf(`resource "sysbox_image" "base" {
  substrate = "docker"
  source = "alpine:3.22"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "%s" {
  substrate = "docker"
  image = sysbox_image.base.id
  provider "docker" {
    command = ["sleep", "infinity"]
  }
}
`, strings.ReplaceAll(topology, "-", "_"))
	status, raw := request("POST", "/v1/revisions", map[string]any{"files": map[string]string{"field.sysbox.hcl": hcl}}, "")
	require.Equal(t, 201, status, string(raw))
	var revision map[string]string
	require.NoError(t, json.Unmarshal(raw, &revision))
	applyBody := map[string]any{"revision": revision["revision"]}
	status, raw = request("POST", "/v1/topologies/"+topology+"/apply", applyBody, "request-one")
	require.Equal(t, 202, status, string(raw))
	var accepted map[string]any
	require.NoError(t, json.Unmarshal(raw, &accepted))
	runID := accepted["run_id"].(string)
	var completion controlplane.RunCompletion
	select {
	case completion = <-lost:
	case <-ctx.Done():
		t.Fatal("timed out waiting for actual agent completion")
	}
	require.Equal(t, controlplane.RunDone, completion.Run.Status, completion.Run.Err)
	localRevision, err := bridge.PinnedRevisionWorkspace(ctx, completion.Run, &controlplane.GlobalRevision{Revision: revision["revision"], Files: map[string][]byte{"field.sysbox.hcl": []byte(hcl)}})
	require.NoError(t, err)
	require.NotEqual(t, completion.Run.SnapshotPath, localRevision, "execution must materialize the revision on the agent")
	state, err := repository.LoadState(ctx, topology)
	require.NoError(t, err)
	var externalID string
	for _, resource := range state.Resources {
		if resource.Address.Type == "sysbox_node" {
			externalID = resource.ExternalID
			require.EqualValues(t, 1, resource.Generation)
		}
	}
	require.NotEmpty(t, externalID)
	inspected, err := cli.ContainerInspect(ctx, externalID)
	require.NoError(t, err)
	require.True(t, inspected.State.Running)
	restarted := NewServerWithConfig(cfg)
	require.NoError(t, restarted.initErr)
	current.Store(restarted)
	select {
	case <-acknowledged:
	case <-time.After(5 * time.Second):
		t.Fatal("completion retry was not acknowledged by restarted API")
	}
	stopAgent()
	select {
	case <-agentDone:
	case <-time.After(5 * time.Second):
		t.Fatal("original agent did not stop")
	}
	agentCtx, stopAgent = context.WithCancel(ctx)
	agentDone = make(chan error, 1)
	reopenedBridge := NewExecutionBridge(agentCfg)
	go func() { agentDone <- agentexec.Run(agentCtx, opts, reopenedBridge) }()
	status, raw = request("POST", "/v1/topologies/"+topology+"/apply", applyBody, "request-one")
	require.Equal(t, 202, status, string(raw))
	var replay map[string]any
	require.NoError(t, json.Unmarshal(raw, &replay))
	require.Equal(t, runID, replay["run_id"])
	stored, err := restarted.apiStore.GetRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunDone, stored.Status)
	instances, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, externalID, instances[0].ID)
	status, raw = request("GET", "/v1/topologies/"+topology, nil, "")
	require.Equal(t, 200, status, string(raw))
	var workspace WorkspaceInfo
	require.NoError(t, json.Unmarshal(raw, &workspace))
	require.False(t, workspace.HasState, "the API must not invent owning-agent state in its own empty repository")
	require.Equal(t, opts.ID, workspace.PlacementAgentID)
	require.Equal(t, controlplane.PhaseUnknown, workspace.Status.Phase)
	status, raw = request("POST", "/v1/agents/"+opts.ID+"/runs/"+runID+"/claim", controlplane.RunClaim{QueuedAt: stored.QueuedAt.Add(-time.Second), Revision: stored.Revision}, "")
	require.Equal(t, 409, status, string(raw))
	stale := completion
	stale.Run.LeaseOwner = "superseded-worker"
	status, raw = request("POST", "/v1/agents/"+opts.ID+"/runs/"+runID+"/complete", stale, "")
	require.Equal(t, 409, status, string(raw))
	status, raw = request("POST", "/v1/runs/"+runID+"/remote/observe", nil, "")
	require.Equal(t, 202, status, string(raw))
	var observation map[string]string
	require.NoError(t, json.Unmarshal(raw, &observation))
	var report controlplane.RunRecoveryReport
	require.Eventually(t, func() bool {
		code, data := request("GET", "/v1/runs/"+runID+"/remote-reports/"+observation["command_id"], nil, "")
		if code != 200 {
			return false
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Errorf("decode owning-agent report: %v", err)
			return false
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)
	var observed bool
	for _, resource := range report.Resources {
		if resource.ExternalID == externalID {
			observed = true
			require.Equal(t, "present", resource.Status)
			require.Equal(t, initial.TopologyUID, resource.OwnerUID)
			require.EqualValues(t, 1, resource.Generation)
			require.NotEmpty(t, resource.ResourceUID)
		}
	}
	require.True(t, observed, "owning-agent live observation must identify the exact Docker container")
	drop.Store(true)
	status, raw = request("POST", "/v1/topologies/"+topology+"/destroy", nil, "destroy-one")
	require.Equal(t, 202, status, string(raw))
	var destruction map[string]any
	require.NoError(t, json.Unmarshal(raw, &destruction))
	destroyID := destruction["run_id"].(string)
	var destroyed *controlplane.Run
	require.Eventually(t, func() bool {
		r, err := current.Load().apiStore.GetRun(ctx, destroyID)
		if err != nil || !r.Status.IsTerminal() {
			return false
		}
		destroyed = r
		return true
	}, 20*time.Second, 50*time.Millisecond)
	require.Equal(t, controlplane.RunDone, destroyed.Status, destroyed.Err)
	require.NotNil(t, destroyed.Cleanup, "destroy completion must persist an owning-agent empty-state receipt")
	select {
	case report := <-lost:
		require.Equal(t, destroyID, report.Run.ID)
		require.NotNil(t, report.Cleanup)
	case <-ctx.Done():
		t.Fatal("destroy completion response was not dropped")
	}
	require.Equal(t, initial.TopologyUID, destroyed.Cleanup.TopologyUID)
	require.Greater(t, destroyed.Cleanup.StateSerial, int64(0))
	instances, err = cli.ContainerList(ctx, container.ListOptions{All: true, Filters: owned})
	require.NoError(t, err)
	require.Empty(t, instances)
	// A cold API must use the persisted receipt, not a memory projection.
	third := NewServerWithConfig(cfg)
	require.NoError(t, third.initErr)
	current.Store(third)
	select {
	case <-acknowledged:
	case <-time.After(10 * time.Second):
		t.Fatal("destroy receipt retry was not acknowledged after API restart")
	}
	status, raw = request("DELETE", "/v1/topologies/"+topology, nil, "")
	require.Equal(t, 200, status, string(raw))
	placement, err := third.workspaceService().loadPlacement(topology)
	require.NoError(t, err)
	require.Nil(t, placement)
	t.Logf("real agent created %s; completion acknowledgment lost; restarted API replay retained run %s and one container; stale claim/report rejected", externalID, runID)
	t.Log("owning-agent destroy committed cleanup receipt; cold API deleted metadata without force")
}
