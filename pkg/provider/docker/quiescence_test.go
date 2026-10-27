package docker

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDockerUnknownOperationFailsClosed(t *testing.T) {
	s := &Substrate{}
	_, err := s.ProveQuiescence(context.Background(), substrate.NodeHandle{ID: "container"}, "op", substrate.OperationIdentity{})
	require.ErrorIs(t, err, substrate.ErrQuiescenceUnsupported)
}
