package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

func TestGCDeletionRequiresExactIDAndConfirmedAbsence(t *testing.T) {
	for _, kind := range []string{"container", "network"} {
		for _, scenario := range []string{"absent", "still-present", "unavailable", "replacement"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				var deletes, reads atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodDelete {
						deletes.Add(1)
						w.WriteHeader(204)
						return
					}
					reads.Add(1)
					if deletes.Load() > 0 {
						if scenario == "absent" {
							http.Error(w, `{"message":"not found"}`, 404)
							return
						}
						if scenario == "unavailable" {
							http.Error(w, `{"message":"daemon unavailable"}`, 500)
							return
						}
					}
					id := "exact-id"
					if scenario == "replacement" {
						id = "different-id"
					}
					labels := map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": "owner", "sysbox.topology_uid": "owner", "sysbox.resource_uid": "opaque-uid", "sysbox.generation": strconv.Itoa(2)}
					body := map[string]any{"Id": id, "Labels": labels}
					if kind == "container" {
						body["Config"] = map[string]any{"Labels": labels}
					}
					require.NoError(t, json.NewEncoder(w).Encode(body))
				}))
				defer server.Close()
				cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
				require.NoError(t, err)
				defer cli.Close()
				s := &Substrate{cli: cli}
				if kind == "container" {
					err = s.DeleteManagedExternal(context.Background(), "exact-id", "owner", "opaque-uid", 2)
				} else {
					err = s.DeleteManagedNetworkExternal(context.Background(), "exact-id", "owner", "opaque-uid", 2)
				}
				if scenario == "absent" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if scenario == "replacement" {
					require.Zero(t, deletes.Load())
				} else {
					require.EqualValues(t, 1, deletes.Load())
					require.EqualValues(t, 2, reads.Load())
				}
			})
		}
	}
}
