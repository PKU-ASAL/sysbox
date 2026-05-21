package api

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/filters"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
)

func TestCleanupCandidateRequiresDoneSupportedUnrecordedResource(t *testing.T) {
	step := runtime.OperationStep{
		Kind:     "resource",
		Provider: "docker",
		Status:   runtime.OperationDone,
	}
	require.True(t, cleanupCandidate(step))

	step.StateRecorded = true
	require.False(t, cleanupCandidate(step))

	step.StateRecorded = false
	step.Status = runtime.OperationFailed
	require.False(t, cleanupCandidate(step))

	step.Status = runtime.OperationDone
	step.Provider = "firecracker"
	require.False(t, cleanupCandidate(step))

	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_node", Name: "vm", Provider: "firecracker"}
	require.True(t, cleanupCandidate(step))

	step.Provider = "network"
	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_network", Name: "lan", Provider: "network"}
	require.True(t, cleanupCandidate(step))

	step.Provider = "libvirt"
	require.False(t, cleanupCandidate(step))
}

func TestFindDockerObjectByLabelsUsesManagedTopologyResourceLabels(t *testing.T) {
	labels := map[string]string{
		runtime.LabelManaged:  "true",
		runtime.LabelTopology: "mixed",
		runtime.LabelResource: "sysbox_node.web",
	}

	var got filters.Args
	id, err := findDockerObjectByLabels(context.Background(), labels, func(args filters.Args) ([]string, error) {
		got = args
		return []string{"container-1"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "container-1", id)
	require.Contains(t, got.Get("label"), runtime.LabelManaged+"=true")
	require.Contains(t, got.Get("label"), runtime.LabelTopology+"=mixed")
	require.Contains(t, got.Get("label"), runtime.LabelResource+"=sysbox_node.web")
}
