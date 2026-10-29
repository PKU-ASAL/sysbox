package api

import (
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostexec"
	"github.com/stretchr/testify/require"
)

func TestExecutionBridgeDoesNotRecoverMissingSnapshotFromMutableWorkspace(t *testing.T) {
	b := &hostexec.Environment{}
	require.Empty(t, b.HCLFileForRun(&controlplane.Run{ID: "old", Topology: "lab"}))
}
