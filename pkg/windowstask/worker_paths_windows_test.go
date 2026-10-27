package windowstask

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWindowsEnvironmentHasFinalDoubleNull(t *testing.T) {
	env, err := windowsEnvironment(map[string]string{"SYSBOX_TEST": "value"})
	require.NoError(t, err)
	require.Greater(t, len(env), 2)
	require.Equal(t, uint16(0), env[len(env)-1])
	require.Equal(t, uint16(0), env[len(env)-2])
}
