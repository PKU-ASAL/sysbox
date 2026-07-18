package runtime

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/filters"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type checkpointAliasNIC struct {
	requests []driver.AttachmentRequest
	err      error
}

func (n *checkpointAliasNIC) Attach(context.Context, substrate.NodeHandle, driver.AttachmentRequest) (driver.AttachmentResult, error) {
	return driver.AttachmentResult{}, nil
}
func (n *checkpointAliasNIC) Observe(_ context.Context, _ substrate.NodeHandle, request driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	n.requests = append(n.requests, request)
	return driver.AttachmentResult{Driver: "docker", State: raw}, n.err
}
func (n *checkpointAliasNIC) Delete(context.Context, substrate.NodeHandle, driver.AttachmentRequest, json.RawMessage) error {
	return nil
}

func TestObserveRecoveredGuestNetworkReportsDrift(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	sub := &portTestSubstrate{
		name:             "recovery-guest-init",
		guestInitModes:   []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit},
		guestObservation: substrate.GuestNetworkInitObservation{Mode: substrate.GuestNetworkInitCloudInit, Converged: false, Reason: "address missing"},
	}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: sub.name, Version: "test", GuestNetworkInit: sub}))

	drifted, reason, err := observeRecoveredGuestNetwork(context.Background(), sub, substrate.NodeHandle{ID: "node"}, sub.name)

	require.NoError(t, err)
	require.True(t, drifted)
	require.Equal(t, "address missing", reason)

	sub.guestInitModes = nil
	driver.DefaultRegistry = driver.NewRegistry()
	drifted, reason, err = observeRecoveredGuestNetwork(context.Background(), sub, substrate.NodeHandle{ID: "node"}, sub.name)
	require.NoError(t, err)
	require.False(t, drifted)
	require.Empty(t, reason)
}

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

func TestRecoverDockerNodeLikeObservesPersistedAliases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		observeErr error
		status     string
		state      state.ResourceStatus
	}{
		{name: "matching", status: "recovered", state: state.ResourcePresent},
		{name: "missing", observeErr: driver.Wrap(driver.ErrorNotFound, "docker", "attachment network aliases drifted", nil), status: "recovered_drifted", state: state.ResourceDrifted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			nic := &checkpointAliasNIC{err: tc.observeErr}
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", NIC: nic}))
			rec := StateResourceLog{Type: "sysbox_node", Name: "mongo", Provider: "docker", Instance: map[string]any{}, Attachments: []state.Attachment{{
				Name: "app", Node: address.Resource("sysbox_node", "mongo"), Network: address.Resource("sysbox_network", "app"), Aliases: []string{"mongo", "database"}, DriverState: json.RawMessage(`{"network_id":"net-1"}`),
			}}}
			st := &state.State{Version: state.SchemaVersion}
			action := CheckpointRecoverResult{Resource: address.Resource("sysbox_node", "mongo").String()}
			require.Equal(t, []string{"mongo", "database"}, StateResourceFromLog(rec).Attachments[0].Aliases)

			result, err := recoverObservedDockerNodeLike(context.Background(), st, &rec, "container-id", action)

			require.NoError(t, err)
			require.Equal(t, tc.status, result.Status)
			require.Equal(t, []string{"mongo", "database"}, nic.requests[0].Aliases)
			require.Equal(t, tc.state, st.FindResource(address.Resource("sysbox_node", "mongo")).Status)
		})
	}
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
