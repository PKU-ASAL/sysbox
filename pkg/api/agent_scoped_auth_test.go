package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestAgentSignatureCannotBecomeAdministratorToken(t *testing.T) {
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.API.Token = "admin-secret"
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	require.NoError(t, s.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "worker", AuthSecret: "worker-secret", SecretHash: agent.SecretHash("worker-secret")}))
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{{"POST", "/v1/agents/worker/execution-session", true}, {"PATCH", "/v1/agents/worker", false}, {"GET", "/v1/topologies", false}, {"POST", "/v1/agents/other/execution-session", false}, {"POST", "/v1/topologies/lab/destroy", false}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		require.NoError(t, agent.SignRequest(r, "worker", "worker-secret", time.Now()))
		w := httptest.NewRecorder()
		authMiddleware(s).ServeHTTP(w, r)
		if test.allowed {
			require.NotEqual(t, http.StatusUnauthorized, w.Code, test.path)
		} else {
			require.Equal(t, http.StatusUnauthorized, w.Code, test.path)
		}
	}
}
