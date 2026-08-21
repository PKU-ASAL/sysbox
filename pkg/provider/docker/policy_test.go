package docker

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestResolvePolicyDeviceMatchesAddressWithoutGuestTooling(t *testing.T) {
	interfaces := []policyInterface{
		{Name: "lo", Addresses: []*net.IPNet{{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}},
		{Name: "eth0", Addresses: []*net.IPNet{{IP: net.ParseIP("10.0.5.30"), Mask: net.CIDRMask(24, 32)}}},
	}

	device, err := resolvePolicyDevice("10.0.5.30/24", interfaces)
	require.NoError(t, err)
	require.Equal(t, "eth0", device)

	_, err = resolvePolicyDevice("10.0.5.31/24", interfaces)
	require.ErrorContains(t, err, "no interface has IP 10.0.5.31")
}

func TestDecodeDockerPolicyTargetRequiresContainerAndBindings(t *testing.T) {
	_, err := decodeDockerPolicyTarget(driver.PolicyTarget{State: json.RawMessage(`{"bindings":{}}`)})
	require.ErrorContains(t, err, "container_id")

	target, err := decodeDockerPolicyTarget(driver.PolicyTarget{State: json.RawMessage(`{"container_id":"router-1","bindings":{"inside":"eth1","uplink":"eth0"}}`)})
	require.NoError(t, err)
	require.Equal(t, "router-1", target.ContainerID)
	require.Equal(t, "eth1", target.Bindings["inside"])
}
