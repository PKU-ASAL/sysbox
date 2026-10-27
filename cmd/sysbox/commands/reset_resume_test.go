package commands

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResetResumeRejectsTargetBeforeWorkspaceRead(t *testing.T) {
	oldResume, oldTarget := flagResetResume, flagResetTarget
	t.Cleanup(func() { flagResetResume, flagResetTarget = oldResume, oldTarget })
	flagResetResume, flagResetTarget = "original", "sysbox_node.web"
	require.ErrorContains(t, runReset(resetCmd, nil), "--resume and --target cannot be combined")
}
