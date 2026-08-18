package libvirt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestLibvirtImplementsConsoleProvider(t *testing.T) {
	var sub any = &Substrate{}
	_, ok := sub.(substrate.ConsoleProvider)
	require.True(t, ok)
}

func TestLibvirtImplementsGuestOperationProviders(t *testing.T) {
	var sub any = &Substrate{}
	_, execOK := sub.(interface {
		ExecInNode(context.Context, substrate.NodeHandle, substrate.ExecRequest) (substrate.ExecResult, error)
		ExecBackground(context.Context, substrate.NodeHandle, substrate.ExecRequest) (int, error)
	})
	_, filesOK := sub.(interface {
		CopyToNode(context.Context, substrate.NodeHandle, string, string, uint32) error
	})
	require.True(t, execOK)
	require.True(t, filesOK)
}
