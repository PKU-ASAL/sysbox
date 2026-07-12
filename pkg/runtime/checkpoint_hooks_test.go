package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/filters"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
)

func TestFindDockerObjectByLabelsUsesManagedTopologyResourceLabels(t *testing.T) {
	labels := map[string]string{
		LabelManaged:  "true",
		LabelTopology: "mixed",
		LabelResource: "sysbox_node.web",
	}

	var got filters.Args
	id, err := findDockerObjectByLabels(context.Background(), labels, func(args filters.Args) ([]string, error) {
		got = args
		return []string{"container-1"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "container-1", id)
	require.Contains(t, got.Get("label"), LabelManaged+"=true")
	require.Contains(t, got.Get("label"), LabelTopology+"=mixed")
	require.Contains(t, got.Get("label"), LabelResource+"=sysbox_node.web")
}

func TestAdoptStateResourceRewritesExternalIDs(t *testing.T) {
	st := &state.State{Version: state.SchemaVersion}
	AdoptStateResource(st, StateResourceLog{
		Type: "sysbox_node", Name: "web",
		Provider: "docker",
		Instance: map[string]any{
			"container_id": "old",
			"primary_ip":   "10.0.0.2",
		},
	}, "new-container")

	res := st.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, res)
	require.Equal(t, "new-container", res.ContainerID())
	require.Equal(t, "10.0.0.2", res.PrimaryIP())

	AdoptStateResource(st, StateResourceLog{
		Type: "sysbox_network", Name: "egress",
		Provider: "docker",
		Instance: map[string]any{
			"docker_network_id": "old-net",
			"nat":               true,
		},
	}, "new-net")

	net := st.FindResource(address.Resource("sysbox_network", "egress"))
	require.NotNil(t, net)
	require.Equal(t, "new-net", net.DockerNetID())
	require.True(t, net.IsNAT())
}

func TestFirecrackerRecoverableArtifactsChecksProviderExtraAnchors(t *testing.T) {
	dir := t.TempDir()
	vmDir := filepath.Join(dir, "vm")
	require.NoError(t, os.Mkdir(vmDir, 0755))

	res := state.Resource{}
	require.NoError(t, res.SetProviderState([]byte(`{"vm_dir":"`+vmDir+`"}`)))
	require.True(t, FirecrackerRecoverableArtifacts(res))

	require.NoError(t, res.SetProviderState([]byte(`{"vm_dir":"`+filepath.Join(dir, "missing")+`"}`)))
	require.False(t, FirecrackerRecoverableArtifacts(res))
}
