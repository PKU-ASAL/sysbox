package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestRecoverCandidateRequiresDockerDoneUnrecordedStateResource(t *testing.T) {
	step := runtime.OperationStep{
		Kind:          "resource",
		Provider:      "docker",
		Status:        runtime.OperationDone,
		StateResource: &runtime.StateResourceLog{Type: "sysbox_node", Name: "web"},
	}
	require.True(t, recoverCandidate(step))

	step.StateRecorded = true
	require.False(t, recoverCandidate(step))

	step.StateRecorded = false
	step.StateResource = nil
	require.False(t, recoverCandidate(step))
}

func TestAdoptStateResourceRewritesExternalID(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	adoptStateResource(st, runtime.StateResourceLog{
		Type:     "sysbox_node",
		Name:     "web",
		Provider: "docker",
		Instance: map[string]any{
			"container_id": "old",
			"primary_ip":   "10.0.0.2",
		},
	}, "new-container")

	res := st.FindResource("sysbox_node", "web")
	require.NotNil(t, res)
	require.Equal(t, "new-container", res.ContainerID())
	require.Equal(t, "10.0.0.2", res.PrimaryIP())
}

func TestAdoptNetworkRewritesDockerNetworkID(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	adoptStateResource(st, runtime.StateResourceLog{
		Type:     "sysbox_network",
		Name:     "egress",
		Provider: "docker",
		Instance: map[string]any{
			"docker_network_id": "old-net",
			"nat":               true,
		},
	}, "new-net")

	res := st.FindResource("sysbox_network", "egress")
	require.NotNil(t, res)
	require.Equal(t, "new-net", res.DockerNetID())
	require.True(t, res.IsNAT())
}
