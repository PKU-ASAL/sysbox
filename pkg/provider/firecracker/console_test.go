package firecracker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

func TestFirecrackerImplementsConsoleProvider(t *testing.T) {
	var sub any = New("/tmp/kernel", t.TempDir())
	_, ok := sub.(substrate.ConsoleProvider)
	require.True(t, ok)
	require.Equal(t, vsockrpc.Op("console"), vsockrpc.OpConsole)
}
