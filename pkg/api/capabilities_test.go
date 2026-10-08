package api

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRuntimeCapabilitiesRequireAuthenticatedSupportedBackend(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.MustLoadServiceConfig("")
			cfg.Paths.RunsDir = filepath.Join(dir, "runs")
			cfg.Paths.WorkspacesDir = filepath.Join(dir, "workspaces")
			cfg.State.Backend = ""
			cfg.API.Token = "fixture-token"
			if backend == "sqlite" {
				cfg.State.Backend = "sqlite://" + filepath.Join(dir, "api.db")
			}
			s := NewServerWithConfig(cfg)
			w := httptest.NewRecorder()
			q := httptest.NewRequest("GET", "/v1/capabilities", nil)
			authMiddleware(s).ServeHTTP(w, q)
			require.Equal(t, 401, w.Code)
			q = httptest.NewRequest("GET", "/v1/capabilities", nil)
			q.Header.Set("Authorization", "Bearer fixture-token")
			w = httptest.NewRecorder()
			authMiddleware(s).ServeHTTP(w, q)
			require.Equal(t, 200, w.Code)
			var cap struct {
				Protocol    string `json:"protocol"`
				Apply       bool   `json:"durable_apply"`
				Destroy     bool   `json:"keyed_destroy"`
				Cleanup     bool   `json:"cleanup_inspection"`
				ClaimReplay bool   `json:"claim_replay"`
				Single      bool   `json:"single_api_execution"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cap))
			require.Equal(t, "cyberfield-runtime-v1", cap.Protocol)
			require.Equal(t, backend == "sqlite", cap.Apply)
			require.Equal(t, cap.Apply, cap.Destroy)
			require.Equal(t, cap.Apply, cap.ClaimReplay)
			require.True(t, cap.Cleanup)
			require.True(t, cap.Single)
		})
	}
}
