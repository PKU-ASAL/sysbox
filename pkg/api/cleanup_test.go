package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/runtime"
)

func TestCleanupCandidateRequiresDoneSupportedUnrecordedResource(t *testing.T) {
	step := runtime.OperationStep{
		Kind:     "resource",
		Provider: "docker",
		Status:   runtime.OperationDone,
		Labels:   map[string]string{runtime.LabelResourceType: "sysbox_node"},
	}
	require.True(t, cleanupCandidate(step))

	step.StateRecorded = true
	require.False(t, cleanupCandidate(step))

	step.StateRecorded = false
	step.Status = runtime.OperationFailed
	require.False(t, cleanupCandidate(step))

	step.Status = runtime.OperationDone
	step.Provider = "firecracker"
	step.Labels = nil
	require.False(t, cleanupCandidate(step))

	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_node", Name: "vm", Provider: "firecracker"}
	require.True(t, cleanupCandidate(step))

	step.Provider = "network"
	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_network", Name: "lan", Provider: "network"}
	require.True(t, cleanupCandidate(step))

	step.Provider = "libvirt"
	step.StateResource = &runtime.StateResourceLog{Type: "sysbox_unknown", Name: "thing", Provider: "libvirt"}
	require.False(t, cleanupCandidate(step))
}
