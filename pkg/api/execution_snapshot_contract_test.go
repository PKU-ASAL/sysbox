package api

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExecutionBridgeDoesNotRecoverMissingSnapshotFromMutableWorkspace(t *testing.T) {
	b := &ExecutionBridge{server: NewServer(t.TempDir(), t.TempDir())}
	require.Empty(t, b.HCLFileForRun(&controlplane.Run{ID: "old", Topology: "lab"}))
}
