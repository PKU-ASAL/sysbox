package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResetCommandIsRegisteredWithExactNodeTarget(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"reset"})
	require.NoError(t, err)
	require.Equal(t, "reset", cmd.Name())
	require.NotNil(t, cmd.Flags().Lookup("target"))
	require.Contains(t, cmd.Use, "--target sysbox_node.name")
}
