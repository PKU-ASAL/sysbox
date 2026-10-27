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
	identity := testOperation()
	ctx := substrate.WithOperationIdentity(context.Background(), identity)
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactOCI, Source: "alpine:latest", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{
		Current: substrate.NodeHandle{ID: "old-id", Generation: 1, Provider: &HandleState{ContainerName: "lab-node"}},
		Node: substrate.NodeSpec{Name: "lab-node", Image: substrate.ArtifactHandle{ID: baseline.Digest, Identity: baseline}, ManagedNetwork: true, Env: map[string]string{"TOKEN": "super-secret"}, ProviderConfig: &Config{Command: OptionalArgv{Set: true, Value: []string{"mongod", "--bind_ip", "0.0.0.0"}}}, Labels: map[string]string{
			"sysbox.managed": "true", "sysbox.topology": "lab", "sysbox.resource": "sysbox_node.web", "sysbox.run_id": "reset-run",
		}},
		Baseline: baseline,
	}

	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	handle.Request = request
	prepared, err := sub.ObserveReset(ctx, handle)
	require.NoError(t, err, "the verified old container still occupying its name is a prepared reset, not a foreign replacement")
	require.Equal(t, substrate.ResetPhasePrepared, prepared.Phase)
	require.Empty(t, prepared.NewExternalID)
	require.False(t, prepared.Converged)
	old := api.containers["old-id"]
	old.labels["sysbox.generation"] = "999"
	_, err = sub.ObserveReset(ctx, handle)
	require.Error(t, err, "old identity branch must still enforce the recorded generation")
	old.labels["sysbox.generation"] = "1"
	preparedState := handle.Provider.(*resetHandleState)
	preparedState.NewContainerID = "new-id"
	_, err = sub.ObserveReset(ctx, handle)
	require.Error(t, err, "a recorded replacement forbids the old object reappearing")
	preparedState.NewContainerID = ""
	require.NoError(t, sub.DestroyReset(ctx, handle))
	created, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, "new-id", created.ID)
	retried, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, created.ID, retried.ID)
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	restored, err := sub.UnmarshalResetHandle(raw)
	require.NoError(t, err)
	restored.Request = request
	adopted, err := sub.ApplyReset(ctx, restored)
	require.NoError(t, err)
	providerState := adopted.Provider.(*HandleState)
	require.True(t, providerState.RemoveDefaultBridge)
	require.Equal(t, []string{"mongod", "--bind_ip", "0.0.0.0"}, providerState.ImageCmd)
	require.Equal(t, []string{"/entry"}, providerState.ImageEntrypoint)
	require.False(t, api.containers["new-id"].running)
	observation, err := sub.ObserveReset(ctx, handle)
	require.NoError(t, err)
	require.True(t, observation.Converged)
	require.Empty(t, observation.Residue)
	require.Equal(t, baseline.Digest, observation.BaselineDigest)
	require.NoError(t, sub.CleanupReset(ctx, handle))
	require.Equal(t, identity.Generation, created.Generation)
	require.Equal(t, "owner", api.containers["new-id"].labels["sysbox.owner_uid"])
	require.Equal(t, "3", api.containers["new-id"].labels["sysbox.generation"])
	require.NotEmpty(t, api.containers["new-id"].labels["sysbox.reset_token"])
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
	api := &resetDockerAPI{containers: map[string]*resetDockerContainer{
		"old-id": {id: "old-id", name: "lab-node", image: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", labels: map[string]string{"sysbox.managed": "true", "sysbox.topology": "lab", "sysbox.resource": "sysbox_node.web", "sysbox.run_id": "apply-run"}, running: true},
	}}
	old := testOperation()
	old.Generation = 1
	for key, value := range operationLabels(old) {
		api.containers["old-id"].labels[key] = value
	}
	return api
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

func resetFixture(t *testing.T) (*Substrate, *resetDockerAPI, context.Context, substrate.ResetRequest) {
	t.Helper()
	api := newResetDockerAPI()
	sub := managedDockerClient(t, api.ServeHTTP)
	identity := testOperation()
	ctx := substrate.WithOperationIdentity(context.Background(), identity)
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactOCI, Digest: "sha256:" + strings.Repeat("a", 64), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{Current: substrate.NodeHandle{ID: "old-id", Generation: 1}, Node: substrate.NodeSpec{Name: "lab-node", Image: substrate.ArtifactHandle{ID: baseline.Digest, Identity: baseline}, Labels: map[string]string{"sysbox.managed": "true", "sysbox.topology": "lab", "sysbox.resource": "sysbox_node.web"}}, Baseline: baseline}
	return sub, api, ctx, request
}

func TestDockerResetRejectsWrongOldGeneration(t *testing.T) {
	sub, api, ctx, request := resetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	api.containers["old-id"].labels["sysbox.generation"] = "9"
	require.Error(t, sub.DestroyReset(ctx, handle))
	require.NotNil(t, api.containers["old-id"])
	require.Error(t, sub.CleanupReset(ctx, handle))
	require.NotNil(t, api.containers["old-id"])
}

func TestDockerResetRejectsReplacementWithChangedIDOrToken(t *testing.T) {
	sub, api, ctx, request := resetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	_, err = sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	api.containers["new-id"].id = "foreign-id"
	_, err = sub.ApplyReset(ctx, handle)
	require.Error(t, err)
	api.containers["new-id"].id = "new-id"
	api.containers["new-id"].labels["sysbox.reset_token"] = "foreign-token"
	_, err = sub.ApplyReset(ctx, handle)
	require.Error(t, err)
	_, err = sub.ObserveReset(ctx, handle)
	require.Error(t, err)
}

func TestDockerResetRequiresOperationIdentityAndRejectsLegacyFormat(t *testing.T) {
	sub, _, _, request := resetFixture(t)
	_, err := sub.PrepareReset(context.Background(), request)
	require.Error(t, err)
	_, err = sub.UnmarshalResetHandle(json.RawMessage(`{"version":1}`))
	require.Error(t, err)
}

func TestDockerResetCannotDropOldOwnershipConstraints(t *testing.T) {
	sub, api, ctx, request := resetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	state := handle.Provider.(*resetHandleState)
	state.Ownership = map[string]string{"sysbox.managed": "true"}
	api.containers["old-id"].labels["sysbox.owner_uid"] = "foreign"
	require.Error(t, sub.DestroyReset(ctx, handle))
	require.NotNil(t, api.containers["old-id"])
}

func TestDockerResetManifestBaselineUsesPinnedLocalImage(t *testing.T) {
	sub, api, ctx, request := resetFixture(t)
	localID := request.Node.Image.ID
	manifest := "sha256:" + strings.Repeat("b", 64)
	request.Baseline.Digest = manifest
	request.Node.Image.Identity.Digest = manifest
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	_, err = sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	cold, err := sub.UnmarshalResetHandle(raw)
	require.NoError(t, err)
	cold.Request = request
	_, err = sub.ApplyReset(ctx, cold)
	require.NoError(t, err)
	observed, err := sub.ObserveReset(ctx, cold)
	require.NoError(t, err)
	require.True(t, observed.Converged)
	require.Equal(t, manifest, observed.BaselineDigest)
	require.Equal(t, localID, api.containers["new-id"].image)
	api.containers["new-id"].image = "sha256:" + strings.Repeat("c", 64)
	_, err = sub.ApplyReset(ctx, cold)
	require.Error(t, err)
	observed, err = sub.ObserveReset(ctx, cold)
	require.NoError(t, err)
	require.False(t, observed.Converged)
	cold.Request.Node.Image.ID = api.containers["new-id"].image
	_, err = sub.ApplyReset(ctx, cold)
	require.Error(t, err, "injected request cannot change prepared local image")
}

func TestDockerResetRejectsMutableLocalImage(t *testing.T) {
	sub, _, ctx, request := resetFixture(t)
	request.Node.Image.ID = "repo:latest"
	_, err := sub.PrepareReset(ctx, request)
	require.Error(t, err)
}
func TestDockerResetLegacyHandleRetainsExactImageBinding(t *testing.T) {
	sub, _, ctx, request := resetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	handle.Provider.(*resetHandleState).LocalImageID = ""
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	cold, err := sub.UnmarshalResetHandle(raw)
	require.NoError(t, err)
	cold.Request = request
	_, err = sub.ObserveReset(ctx, cold)
	require.NoError(t, err)
	cold.Request.Node.Image.ID = "sha256:" + strings.Repeat("d", 64)
	require.Error(t, sub.DestroyReset(ctx, cold))
}
