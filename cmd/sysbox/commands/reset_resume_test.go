package commands

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResetResumeRejectsChangedTarget(t *testing.T) {
	cmd := newLifecycleCommand("reset")
	cmd.SetArgs([]string{"--resume", "old-run", "--target", "sysbox_node.web"})
	require.ErrorContains(t, cmd.Execute(), "--resume retains")
}
