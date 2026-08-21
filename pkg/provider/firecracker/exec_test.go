package firecracker

import (
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGuestFileInstallRequestUsesDirectExecution(t *testing.T) {
	request := guestFileInstallRequest("chmod", "0600", "/tmp/payload")

	require.Equal(t, substrate.ShellNone, request.Shell)
	require.NoError(t, request.Validate())
}
