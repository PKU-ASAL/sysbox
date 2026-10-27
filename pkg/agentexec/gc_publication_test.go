package agentexec

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestLocalCLIRejectsEnrolledSourceWithoutPublisher(t *testing.T) {
	b := NewLocalBridge(LocalOptions{Topology: "lab", StatePath: filepath.Join(t.TempDir(), "state.json"), RunsDir: t.TempDir()})
	ctx := context.Background()
	repo, err := b.repository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	_, err = repo.BeginGCSource(ctx, "lab", "remote", st.TopologyUID, 0)
	require.NoError(t, err)
	_, _, err = b.PrepareExecution(ctx, &controlplane.Run{Topology: "lab"})
	require.ErrorContains(t, err, "local CLI")
}

type publicationContextKey struct{}
type publicationBridge struct{ Bridge }

func (publicationBridge) PrepareExecution(ctx context.Context, _ *controlplane.Run) (context.Context, func(), error) {
	return context.WithValue(ctx, publicationContextKey{}, true), func() {}, nil
}
func TestRemoteBridgePreservesArtifactPublicationContext(t *testing.T) {
	b := remoteBridge{Bridge: publicationBridge{}}
	ctx, release, err := b.PrepareExecution(context.Background(), &controlplane.Run{Topology: "lab"})
	require.NoError(t, err)
	defer release()
	require.Equal(t, true, ctx.Value(publicationContextKey{}))
}
