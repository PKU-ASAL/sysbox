package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOCIResolutionRequiresPublicationBeforeDaemonAccess(t *testing.T) {
	denied := errors.New("publication fenced")
	ctx := artifact.WithPublication(context.Background(), func(context.Context, artifact.Spec) (func(artifact.Result) error, error) { return nil, denied })
	s := &Substrate{} // A daemon access before publication would panic.
	_, err := s.ResolveImage(ctx, substrate.ArtifactSource{Kind: substrate.ArtifactOCI, Source: "alpine:3.22"})
	require.ErrorIs(t, err, denied)
}
