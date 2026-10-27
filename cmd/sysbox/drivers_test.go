package main

import (
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDockerRegistrationExposesImplementedOwnershipAdoption(t *testing.T) {
	docker, err := dockerprovider.New()
	require.NoError(t, err)
	defer docker.Close()
	registry := driver.NewRegistry()
	require.NoError(t, registerDockerDriver(registry, docker))
	adoption, err := registry.RequireOwnershipAdoption("docker")
	require.NoError(t, err, "deployed API uses this capability before submitting transfer")
	require.Same(t, docker, adoption)
	for _, capability := range []driver.Capability{driver.CapabilityNode, driver.CapabilityNIC, driver.CapabilityReset} {
		_, err = registry.Require("docker", capability)
		require.NoError(t, err)
	}
}
