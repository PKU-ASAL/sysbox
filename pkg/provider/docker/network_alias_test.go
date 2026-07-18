package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestDockerEndpointSettingsIncludeExactAliases(t *testing.T) {
	settings, err := dockerEndpointSettings("10.77.1.10/24", "02:00:00:00:00:10", []string{"mongo", "database"})

	require.NoError(t, err)
	require.Equal(t, []string{"mongo", "database"}, settings.Aliases)
	require.Equal(t, "10.77.1.10", settings.IPAMConfig.IPv4Address)
	require.Equal(t, "02:00:00:00:00:10", settings.MacAddress)
}

func TestDockerIsolatedAttachmentRejectsAliasesBeforeMutation(t *testing.T) {
	sub := &Substrate{}
	_, err := sub.Attach(context.Background(), substrate.NodeHandle{ID: "node"}, driver.AttachmentRequest{
		Name: "lab", Aliases: []string{"node"}, NetworkState: json.RawMessage(`{"nat":false,"netns":"lab","bridge":"br0"}`),
	})

	require.ErrorContains(t, err, "network aliases require a Docker-managed network")
}

func TestDockerObservedAliasesMustContainDesiredSet(t *testing.T) {
	require.True(t, containsAliases([]string{"database", "mongo", "other"}, []string{"mongo", "database"}))
	require.False(t, containsAliases([]string{"mongo"}, []string{"mongo", "database"}))
}

func TestDockerObserveReportsMissingAliasAsDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Contains(t, r.URL.Path, "/containers/node/json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"NetworkSettings":{"Networks":{"app":{"NetworkID":"net-1","Aliases":["mongo"]}}}}`))
	}))
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithHTTPClient(server.Client()), client.WithVersion("1.43"))
	require.NoError(t, err)
	sub := &Substrate{cli: cli}
	raw := json.RawMessage(`{"kind":"docker-nat","network_id":"net-1"}`)

	_, err = sub.Observe(context.Background(), substrate.NodeHandle{ID: "node"}, driver.AttachmentRequest{
		Name: "app", Aliases: []string{"mongo", "database"},
	}, raw)

	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
	require.ErrorContains(t, err, "network aliases drifted")
}
