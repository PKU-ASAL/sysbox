package libvirt

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestLibvirtImplementsConsoleProvider(t *testing.T) {
	var sub any = &Substrate{}
	_, ok := sub.(substrate.ConsoleProvider)
	require.True(t, ok)
}
