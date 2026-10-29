package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestExecutionBridgeDoesNotRecoverMissingSnapshotFromMutableWorkspace(t *testing.T) {
	b := &ExecutionBridge{}
	require.Empty(t, b.HCLFileForRun(&controlplane.Run{ID: "old", Topology: "lab"}))
}
