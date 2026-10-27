package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLibvirtUnknownOperationFailsClosed(t *testing.T) {
	s := New(t.TempDir())
	_, err := s.ProveQuiescence(context.Background(), substrate.NodeHandle{ID: "domain"}, "op", substrate.OperationIdentity{})
	require.ErrorIs(t, err, substrate.ErrQuiescenceUnsupported)
}
