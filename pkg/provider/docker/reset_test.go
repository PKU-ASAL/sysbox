package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestDockerResetRecreatesPinnedOwnedContainerAndHidesSecrets(t *testing.T) {
	api := newResetDockerAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithHTTPClient(server.Client()), client.WithVersion("1.43"))
	require.NoError(t, err)
	sub := &Substrate{cli: cli}
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactOCI, Source: "alpine:latest", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{
		Current: substrate.NodeHandle{ID: "old-id", Provider: &HandleState{ContainerName: "lab-node"}},
		Node: substrate.NodeSpec{Name: "lab-node", Image: substrate.ArtifactHandle{ID: baseline.Digest, Identity: baseline}, ManagedNetwork: true, Env: map[string]string{"TOKEN": "super-secret"}, ProviderConfig: &Config{Command: OptionalArgv{Set: true, Value: []string{"mongod", "--bind_ip", "0.0.0.0"}}}, Labels: map[string]string{
			"sysbox.managed": "true", "sysbox.topology": "lab", "sysbox.resource": "sysbox_node.web", "sysbox.run_id": "reset-run",
		}},
		Baseline: baseline,
	}

	handle, err := sub.PrepareReset(context.Background(), request)
	require.NoError(t, err)
	handle.Request = request
	require.NoError(t, sub.DestroyReset(context.Background(), handle))
	created, err := sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	require.Equal(t, "new-id", created.ID)
	retried, err := sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	require.Equal(t, created.ID, retried.ID)
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	restored, err := sub.UnmarshalResetHandle(raw)
	require.NoError(t, err)
	restored.Request = request
	adopted, err := sub.ApplyReset(context.Background(), restored)
	require.NoError(t, err)
	providerState := adopted.Provider.(*HandleState)
	require.True(t, providerState.RemoveDefaultBridge)
	require.Equal(t, []string{"mongod", "--bind_ip", "0.0.0.0"}, providerState.ImageCmd)
	require.Equal(t, []string{"/entry"}, providerState.ImageEntrypoint)
	require.False(t, api.containers["new-id"].running)
	observation, err := sub.ObserveReset(context.Background(), handle)
	require.NoError(t, err)
	require.True(t, observation.Converged)
	require.Empty(t, observation.Residue)
	require.Equal(t, baseline.Digest, observation.BaselineDigest)
	require.NoError(t, sub.CleanupReset(context.Background(), handle))
	require.NotContains(t, string(raw), "super-secret")
	require.NotContains(t, string(raw), "TOKEN")
}

type resetDockerContainer struct {
	id, name, image string
	labels          map[string]string
	running         bool
}

type resetDockerAPI struct {
	containers map[string]*resetDockerContainer
}

func newResetDockerAPI() *resetDockerAPI {
	return &resetDockerAPI{containers: map[string]*resetDockerContainer{
		"old-id": {id: "old-id", name: "lab-node", image: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", labels: map[string]string{"sysbox.managed": "true", "sysbox.topology": "lab", "sysbox.resource": "sysbox_node.web", "sysbox.run_id": "apply-run"}, running: true},
	}}
}

func (a *resetDockerAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if i := strings.Index(path, "/containers/"); i >= 0 {
		rest := path[i+len("/containers/"):]
		id := strings.Split(rest, "/")[0]
		container := a.lookup(id)
		switch {
		case strings.HasSuffix(rest, "/json") && r.Method == http.MethodGet:
			if container == nil {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": container.id, "Name": "/" + container.name, "Image": container.image, "Config": map[string]any{"Labels": container.labels}, "State": map[string]any{"Running": container.running, "Status": "created"}, "NetworkSettings": map[string]any{"Networks": map[string]any{}}})
			return
		case strings.HasSuffix(rest, "/stop") && r.Method == http.MethodPost:
			if container != nil {
				container.running = false
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case r.Method == http.MethodDelete:
			if container == nil {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			delete(a.containers, container.id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json") {
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Config": map[string]any{"Cmd": []string{"echo", "ready"}, "Entrypoint": []string{"/entry"}}})
		return
	}
	if strings.HasSuffix(path, "/containers/create") && r.Method == http.MethodPost {
		var body struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		a.containers["new-id"] = &resetDockerContainer{id: "new-id", name: name, image: body.Image, labels: body.Labels}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "new-id", "Warnings": []string{}})
		return
	}
	http.Error(w, `{"message":"unexpected endpoint"}`, http.StatusNotFound)
}

func (a *resetDockerAPI) lookup(id string) *resetDockerContainer {
	if found := a.containers[id]; found != nil {
		return found
	}
	for _, container := range a.containers {
		if container.name == id {
			return container
		}
	}
	return nil
}
