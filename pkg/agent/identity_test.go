package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveAndLoadIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	ident := &Identity{
		ID:           "host-a",
		APIURL:       "https://sysbox.example",
		Capabilities: []string{"docker"},
	}

	require.NoError(t, SaveIdentity(path, ident))
	loaded, err := LoadIdentity(path)
	require.NoError(t, err)
	require.Equal(t, ident.ID, loaded.ID)
	require.Equal(t, ident.APIURL, loaded.APIURL)
	require.Equal(t, []string{"docker"}, loaded.Capabilities)
}

func TestIdentityAgentProjection(t *testing.T) {
	ident := &Identity{
		ID:           "host-a",
		Name:         "Host A",
		Capabilities: []string{"docker", "network"},
		Labels:       map[string]string{"role": "lab"},
	}

	agent := ident.Agent()
	require.Equal(t, "host-a", agent.ID)
	require.Equal(t, "Host A", agent.Name)
	require.Equal(t, "online", agent.Status)
	require.Equal(t, []string{"docker", "network"}, agent.Capabilities)
	require.Equal(t, "lab", agent.Labels["role"])
	require.False(t, agent.LastHeartbeat.IsZero())
}

func TestRegisterPersistsIdentityAndRegistersRemote(t *testing.T) {
	var got struct {
		ID           string   `json:"id"`
		Capabilities []string `json:"capabilities"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/agents", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "identity.json")
	ident, err := Register(t.Context(), RegisterOptions{
		APIURL:       server.URL,
		Token:        "test-token",
		ID:           "host-a",
		Capabilities: []string{"docker"},
		Path:         path,
	})
	require.NoError(t, err)
	require.Equal(t, "host-a", ident.ID)
	require.Equal(t, "host-a", got.ID)
	require.Equal(t, []string{"docker"}, got.Capabilities)

	loaded, err := LoadIdentity(path)
	require.NoError(t, err)
	require.Equal(t, server.URL, loaded.APIURL)
	require.Equal(t, "test-token", loaded.Token)
}
