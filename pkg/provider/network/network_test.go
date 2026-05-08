package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCIDR(t *testing.T) {
	gw, err := GatewayCIDR("10.0.1.0/24")
	require.NoError(t, err)
	require.Equal(t, "10.0.1.1/24", gw)

	gw, err = GatewayCIDR("192.168.99.0/24")
	require.NoError(t, err)
	require.Equal(t, "192.168.99.1/24", gw)

	_, err = GatewayCIDR("not-a-cidr")
	require.Error(t, err)
}
