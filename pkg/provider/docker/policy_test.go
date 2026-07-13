package docker

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestDecodeDockerPolicyTargetRequiresContainerAndBindings(t *testing.T) {
	_, err := decodeDockerPolicyTarget(driver.PolicyTarget{State: json.RawMessage(`{"bindings":{}}`)})
	require.ErrorContains(t, err, "container_id")

	target, err := decodeDockerPolicyTarget(driver.PolicyTarget{State: json.RawMessage(`{"container_id":"router-1","bindings":{"inside":"eth1","uplink":"eth0"}}`)})
	require.NoError(t, err)
	require.Equal(t, "router-1", target.ContainerID)
	require.Equal(t, "eth1", target.Bindings["inside"])
}
