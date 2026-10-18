package docker

import (
	"context"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFenceLocalityRejectsRemoteDaemon(t *testing.T) {
	for _, host := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375"} {
		cli, err := client.NewClientWithOpts(client.WithHost(host))
		require.NoError(t, err)
		defer cli.Close()
		s := &Substrate{cli: cli}
		scope, err := s.HostFenceScope(context.Background())
		if host == "unix:///var/run/docker.sock" {
			require.NoError(t, err)
			require.Equal(t, "local-only", scope)
		} else {
			require.Error(t, err)
		}
	}
}
