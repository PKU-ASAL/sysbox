//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestRemoteDockerAdoptionInspectionE2E(t *testing.T) {
	if os.Getenv("SYSBOX_REMOTE_DOCKER_E2E") != "1" {
		t.Skip("requires isolated Docker resource")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	private := func() string { dir := t.TempDir(); require.NoError(t, os.Chmod(dir, 0700)); return dir }
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = private()
	cfg.Paths.WorkspacesDir = private()
	cfg.State.Backend = "sqlite://" + filepath.Join(private(), "api.db")
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	s.grantHostKeys["inspect-agent"] = hostPub
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	before, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	api := httptest.NewServer(s)
	defer api.Close()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Cmd: []string{"true"}}, &container.HostConfig{NetworkMode: "none"}, nil, nil, "")
	require.NoError(t, err)
	defer cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	provider, err := dockerprovider.New()
	require.NoError(t, err)
	defer provider.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "1", Node: provider, NodeState: provider, OwnershipAdoption: provider}))
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "inspect-agent", Secret: "inspect-secret", APIURL: api.URL}}
	socket := filepath.Join(private(), "host.sock")
	host := &hostsupervisor.Server{Socket: socket, GroupID: -1, Adoption: &hostsupervisor.AdoptionCoordinator{Root: private(), IssuerKey: pub, HostKey: hostKey, AgentID: "inspect-agent", HostBootID: "boot", Authorize: authority.Authorize, Report: authority.ReportNodeOperation}, AdoptionInspect: func(ctx context.Context, req controlplane.AdoptionInspectionRequest) (substrate.AdoptionEvidence, error) {
		return provider.InspectAdoption(ctx, req.ExternalID, substrate.NodeSpec{})
	}}
	hostPublicFile := wireTestHostObserver(t, host, cfg, "inspect-agent", []string{"docker"}, hostKey)
	hostCtx, stopHost := context.WithCancel(ctx)
	hostDone := make(chan error, 1)
	go func() { hostDone <- host.Serve(hostCtx) }()
	defer func() { stopHost(); require.NoError(t, <-hostDone) }()
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	keyFile := filepath.Join(private(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyFile, pub, 0600))
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	opts := agentexec.Options{ID: "inspect-agent", Secret: "inspect-secret", APIURL: api.URL, Capabilities: []string{"docker"}, PollInterval: 20 * time.Millisecond, ExecutionSessionFile: filepath.Join(private(), "session.json"), ExecutionGrantPublicKeyFile: keyFile, HostSupervisorSocket: socket}
	opts.HostPublicKeyFile = hostPublicFile
	go func() { workerDone <- agentexec.Run(workerCtx, opts) }()
	defer stopWorker()
	require.Eventually(t, func() bool {
		a, err := s.apiStore.GetAgent(ctx, "inspect-agent")
		return err == nil && a != nil && a.IsSchedulable()
	}, 5*time.Second, 20*time.Millisecond)
	body, _ := json.Marshal(map[string]string{"type": "sysbox_node", "name": "imported", "id": created.ID, "substrate": "docker"})
	request := httptest.NewRequest("POST", "/v1/topologies/lab/adoption-inspections", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "inspection-one")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	require.Equal(t, 202, response.Code, response.Body.String())
	var admitted controlplane.NodeOperation
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &admitted))
	var complete *controlplane.NodeOperation
	require.Eventually(t, func() bool {
		complete, err = s.apiStore.GetNodeOperation(ctx, admitted.ID)
		return err == nil && complete.Status == controlplane.NodeOperationStatusDone
	}, 10*time.Second, 20*time.Millisecond)
	require.NotNil(t, complete.InspectionEvidence)
	require.Equal(t, created.ID, complete.InspectionEvidence.ExternalID)
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, before, after, "inspection must not change state")
	actual, err := cli.ContainerInspect(ctx, created.ID)
	require.NoError(t, err)
	require.Empty(t, actual.Config.Labels["sysbox.owner_uid"], "inspection must not claim provider ownership")
	stopWorker()
	select {
	case <-workerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	// Confirmation is explicit and uses stored host evidence, never request JSON.
	confirm := httptest.NewRequest("POST", "/v1/topologies/lab/adoption-inspections/"+admitted.ID+"/confirm", nil)
	confirm.Header.Set("Idempotency-Key", "adopt-one")
	confirmed := httptest.NewRecorder()
	s.ServeHTTP(confirmed, confirm)
	require.Equal(t, 202, confirmed.Code, confirmed.Body.String())
	var adoption controlplane.NodeOperation
	require.NoError(t, json.Unmarshal(confirmed.Body.Bytes(), &adoption))
	require.Equal(t, "adoption_apply", adoption.Operation)
	require.Equal(t, *complete.InspectionEvidence, adoption.Adoption.Evidence)
	t.Log("real Docker inspected through API/Agent/host signature; immutable evidence persisted, provider/state unchanged, explicit confirm dispatched")
}
