package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type keyPowerDriver struct{ driver.Power }

func TestNodePowerKeySurvivesRestartAndTargetReplacement(t *testing.T) {
	original := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = original }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "key-power", Version: "test", Power: keyPowerDriver{}}))
	for _, backend := range []string{"file", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.DefaultServiceConfig()
			cfg.Paths.RunsDir = t.TempDir()
			cfg.Paths.WorkspacesDir = t.TempDir()
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(t.TempDir(), "state.db")
			}
			if backend == "postgres" {
				cfg.State.Backend = isolatedPostgresTestDSN(t)
			}
			servers := []*Server{NewServerWithConfig(cfg), NewServerWithConfig(cfg)}
			s := servers[0]
			require.NoError(t, s.initErr)
			manager, err := s.stateManager("lab")
			require.NoError(t, err)
			canonical, err := manager.LoadWithContext(context.Background())
			require.NoError(t, err)
			canonical.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: "key-power", OwnerUID: canonical.TopologyUID, ResourceUID: "original", Generation: 1}}
			require.NoError(t, manager.SaveWithContext(context.Background(), canonical))
			require.NoError(t, s.agentService().Save(context.Background(), controlplane.Agent{ID: "host", Status: controlplane.AgentStatusOnline, Capabilities: []string{"key-power"}}))
			post := func(server *Server, operation, node, key string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/v1/topologies/lab/nodes/"+node+"/"+operation, nil)
				r.Header.Set("Idempotency-Key", key)
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				return w
			}
			var group sync.WaitGroup
			responses := make(chan *httptest.ResponseRecorder, 8)
			for i := range 8 {
				group.Add(1)
				go func() { defer group.Done(); responses <- post(servers[i%2], "pause", "web", "same-key") }()
			}
			group.Wait()
			close(responses)
			id := ""
			for response := range responses {
				require.Equal(t, 202, response.Code, response.Body.String())
				var operation controlplane.NodeOperation
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &operation))
				if id == "" {
					id = operation.ID
				}
				require.Equal(t, id, operation.ID)
			}
			commands, err := s.apiStore.ListAgentCommands(context.Background(), "host")
			require.NoError(t, err)
			require.Len(t, commands, 1)
			canonical.Resources[0].ResourceUID = "replacement"
			canonical.Resources[0].Generation++
			require.NoError(t, manager.SaveWithContext(context.Background(), canonical))
			cold := NewServerWithConfig(cfg)
			replay := post(cold, "pause", "web", "same-key")
			require.Equal(t, 202, replay.Code, replay.Body.String())
			var operation controlplane.NodeOperation
			require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &operation))
			require.Equal(t, id, operation.ID)
			require.Equal(t, "original", operation.ResourceUID)
			require.EqualValues(t, 1, operation.Generation)
			require.Equal(t, 409, post(cold, "resume", "web", "same-key").Code)
			require.Equal(t, 409, post(cold, "pause", "other", "same-key").Code)
			commands, err = s.apiStore.ListAgentCommands(context.Background(), "host")
			require.NoError(t, err)
			require.Len(t, commands, 1)
		})
	}
}
