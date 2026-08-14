package docker

import (
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAtomicRenameRequestUsesDirectExecution(t *testing.T) {
	req := atomicRenameRequest("/etc/.sysbox-proof", "/etc/proof")

	require.Equal(t, "mv", req.Program)
	require.Equal(t, []string{"-f", "/etc/.sysbox-proof", "/etc/proof"}, req.Args)
	require.Equal(t, substrate.ShellNone, req.Shell)
}
