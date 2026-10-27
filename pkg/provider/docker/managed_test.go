package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func managedDockerClient(t *testing.T, handler http.HandlerFunc) *Substrate {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return &Substrate{cli: cli}
}
func testOperation() substrate.OperationIdentity {
	return substrate.OperationIdentity{ActionID: "action", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 3}
}

func TestManagedDockerDiscoveryDistinguishesAbsenceFromUnavailable(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/containers/json") && status == 404 {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				http.Error(w, `{"message":"not available"}`, status)
			})
			_, err := sub.DiscoverManagedNode(context.Background(), testOperation(), "node")
			category := driver.ErrorUnavailable
			if status == 404 {
				category = driver.ErrorNotFound
			}
			require.True(t, driver.IsCategory(err, category), "%v", err)
		})
	}
}

func TestManagedDockerDiscoveryRejectsForeignNameAndDuplicates(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign", true: "duplicate"}[duplicate], func(t *testing.T) {
			sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/containers/json") {
					if duplicate {
						_, _ = w.Write([]byte(`[{"Id":"a"},{"Id":"b"}]`))
					} else {
						_, _ = w.Write([]byte(`[]`))
					}
					return
				}
				_, _ = w.Write([]byte(`{"Id":"foreign","Name":"/node","Config":{"Labels":{"sysbox.managed":"true"}}}`))
			})
			_, err := sub.DiscoverManagedNode(context.Background(), testOperation(), "node")
			require.True(t, driver.IsCategory(err, driver.ErrorInvalidState), "%v", err)
		})
	}
}

func TestDockerCreatePersistsOperationIdentityAndNeverDeletesNameCollision(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "foreign"}[exists], func(t *testing.T) {
			mutations := 0
			var labels map[string]string
			sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					mutations++
				}
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/"):
					if exists {
						_, _ = w.Write([]byte(`{"Id":"foreign","Name":"/node","Config":{"Labels":{"sysbox.managed":"true"}}}`))
					} else {
						http.Error(w, `{"message":"missing"}`, 404)
					}
				case strings.Contains(r.URL.Path, "/images/"):
					_, _ = w.Write([]byte(`{}`))
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					var config struct{ Labels map[string]string }
					_ = json.NewDecoder(r.Body).Decode(&config)
					labels = config.Labels
					_, _ = w.Write([]byte(`{"Id":"new"}`))
				default:
					w.WriteHeader(200)
				}
			})
			handle, err := sub.CreateNode(substrate.WithOperationIdentity(context.Background(), testOperation()), substrate.NodeSpec{Name: "node", Image: substrate.ArtifactHandle{ID: "image"}})
			if exists {
				require.Error(t, err)
				require.Zero(t, mutations)
			} else {
				require.NoError(t, err)
				require.Equal(t, "owner", labels["sysbox.owner_uid"])
				require.Equal(t, "resource", labels["sysbox.resource_uid"])
				require.Equal(t, "3", labels["sysbox.generation"])
				require.EqualValues(t, 3, handle.Generation)
			}
		})
	}
}

func TestDockerNetworkReadAndCreateDoNotMaskUnavailableOrAdoptForeign(t *testing.T) {
	for _, status := range []int{200, 403, 500, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			creates := 0
			sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					creates++
					_, _ = w.Write([]byte(`{"Id":"new"}`))
					return
				}
				if status == 200 {
					_, _ = w.Write([]byte(`{"Id":"foreign","Name":"net","Labels":{"owner":"other"}}`))
					return
				}
				http.Error(w, `{"message":"unavailable"}`, status)
			})
			_, err := sub.ReadManagedNetwork(context.Background(), substrate.ManagedNetworkSpec{Name: "net", Labels: map[string]string{"owner": "mine"}})
			expected := driver.ErrorUnavailable
			if status == 404 {
				expected = driver.ErrorNotFound
			}
			if status == 200 {
				expected = driver.ErrorInvalidState
			}
			require.True(t, driver.IsCategory(err, expected), "%v", err)
			_, err = sub.CreateBridgeNetwork(context.Background(), "net", "10.0.0.0/24", map[string]string{"owner": "mine"})
			if status == 404 {
				require.NoError(t, err)
				require.Equal(t, 1, creates)
			} else {
				require.Error(t, err)
				require.Zero(t, creates)
			}
		})
	}
}

func TestManagedDockerDiscoveryRestoresLaunchStateAndFullID(t *testing.T) {
	labels := map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": "owner", "sysbox.resource_uid": "resource", "sysbox.topology_uid": "topology", "sysbox.generation": "3", "sysbox.action_id": "original-create", "sysbox.node_state": `{"container_name":"node","image_cmd":["serve"],"direct_launch":true}`}
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			if r.URL.Query().Get("all") != "1" {
				http.Error(w, `{"message":"must query stopped containers"}`, 400)
				return
			}
			_, _ = w.Write([]byte(`[{"Id":"complete-container-id"}]`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "complete-container-id", "Name": "/node", "Config": map[string]any{"Labels": labels}})
	})
	identity := testOperation()
	identity.ActionID = "observe"
	handle, err := sub.DiscoverManagedNode(context.Background(), identity, "node")
	require.NoError(t, err)
	require.Equal(t, "complete-container-id", handle.ID)
	require.EqualValues(t, 3, handle.Generation)
	require.Equal(t, []string{"serve"}, handle.Provider.(*HandleState).ImageCmd)
	require.True(t, handle.Provider.(*HandleState).DirectLaunch)
}

func TestManagedNetworkReadByStoredIDDoesNotFallback(t *testing.T) {
	queries := 0
	sub := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries++
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"message":"missing"}`, 404)
	})
	_, err := sub.ReadManagedNetwork(substrate.WithOperationIdentity(context.Background(), testOperation()), substrate.ManagedNetworkSpec{Name: "stored-id"})
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound))
	require.Equal(t, 1, queries)
}
