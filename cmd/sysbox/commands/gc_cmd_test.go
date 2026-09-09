package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestCollectActiveIDs(t *testing.T) {
	runsDir := t.TempDir()

	st := &state.State{Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "container-1"},
		{Address: address.Resource("sysbox_network", "lab"), Driver: "docker", ExternalID: "network-1"},
		{Address: address.Resource("sysbox_node", "vm"), Driver: "libvirt", ExternalID: "domain-1"},
	}}
	require.NoError(t, state.NewManager(filepath.Join(runsDir, "lab", "state.json")).Save(st))

	active := collectActiveIDs(runsDir)
	require.True(t, active.containers["container-1"])
	require.True(t, active.networks["network-1"])
	require.True(t, active.domains["domain-1"])
}

func TestFindOrphansDocker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{ID: "orphan-container", Labels: map[string]string{runtime.LabelManaged: "true", runtime.LabelTopology: "gone"}},
				{ID: "active-container", Labels: map[string]string{runtime.LabelManaged: "true", runtime.LabelTopology: "lab"}},
				{ID: "unmanaged", Labels: map[string]string{}}, // no sysbox.managed, must be ignored
			})
		case strings.Contains(r.URL.Path, "/networks"):
			_ = json.NewEncoder(w).Encode([]network.Summary{
				{ID: "orphan-network", Labels: map[string]string{runtime.LabelManaged: "true"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", server.URL)

	active := activeIDs{
		containers: map[string]bool{"active-container": true},
		networks:   map[string]bool{},
		domains:    map[string]bool{},
	}

	orphans, err := findOrphans(context.Background(), active)
	require.NoError(t, err)

	var kinds []string
	for _, o := range orphans {
		kinds = append(kinds, o.Kind+":"+o.ID)
	}
	require.Contains(t, kinds, "container:orphan-container")
	require.Contains(t, kinds, "network:orphan-network")
	require.NotContains(t, kinds, "container:active-container")
	require.NotContains(t, kinds, "container:unmanaged")
}
