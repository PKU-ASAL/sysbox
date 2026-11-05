package api

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthPublishesBuildAndAgentProtocol(t *testing.T) {
	server := NewServer(t.TempDir(), t.TempDir())
	out := httptest.NewRecorder()
	server.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	require.Equal(t, http.StatusOK, out.Code)
	var report struct {
		Build    buildinfo.Info `json:"build"`
		Protocol string         `json:"agent_protocol"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &report))
	require.Equal(t, buildinfo.Current(), report.Build)
	require.Equal(t, controlplane.AgentProtocolVersion, report.Protocol)
}

func TestWorkerCanCheckBuildWithoutAdministratorToken(t *testing.T) {
	server := NewServer(t.TempDir(), t.TempDir())
	server.cfg.API.Token = "admin"
	require.NoError(t, server.apiStore.SaveAgent(context.Background(), controlplane.Agent{ID: "worker", AuthSecret: "secret", SecretHash: agent.SecretHash("secret")}))
	for _, signed := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		if signed {
			require.NoError(t, agent.SignRequest(req, "worker", "secret", time.Now()))
		}
		out := httptest.NewRecorder()
		authMiddleware(server).ServeHTTP(out, req)
		if signed {
			require.Equal(t, 200, out.Code)
		} else {
			require.Equal(t, 401, out.Code)
		}
	}
}
