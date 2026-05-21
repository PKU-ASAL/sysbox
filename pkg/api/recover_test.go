package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestRecoverCandidateRequiresSupportedDoneUnrecordedStateResource(t *testing.T) {
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

	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_node", Name: "vm"}
	step.Provider = "firecracker"
	require.True(t, recoverCandidate(step))

	step.Provider = "network"
	require.True(t, recoverCandidate(step))

	step.Provider = "libvirt"
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

func TestAdoptFirecrackerStateResourceKeepsProviderExtra(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	adoptStateResource(st, runtime.StateResourceLog{
		Type:     "sysbox_node",
		Name:     "vm",
		Provider: "firecracker",
		Instance: map[string]any{
			"container_id":    "sysbox-vm",
			"provider_extra":  `{"vm_dir":"/tmp/sysbox-vm"}`,
			"desired_hash_v2": "hash",
		},
	}, "")

	res := st.FindResource("sysbox_node", "vm")
	require.NotNil(t, res)
	require.Equal(t, "sysbox-vm", res.ContainerID())
	require.Equal(t, `{"vm_dir":"/tmp/sysbox-vm"}`, res.ProviderExtra())
}

func TestFirecrackerRecoverableChecksProviderExtraAnchors(t *testing.T) {
	dir := t.TempDir()
	vmDir := filepath.Join(dir, "vm")
	require.NoError(t, os.Mkdir(vmDir, 0755))

	res := state.Resource{Instance: map[string]any{
		"provider_extra": `{"vm_dir":"` + vmDir + `"}`,
	}}
	require.True(t, firecrackerRecoverable(res))

	res.Instance["provider_extra"] = `{"vm_dir":"` + filepath.Join(dir, "missing") + `"}`
	require.False(t, firecrackerRecoverable(res))
}
