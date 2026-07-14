package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
)

func TestResolvePlanArtifactDigestsDetectsLocalImageMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o644))
	g := graph.New()
	image := address.Resource("sysbox_image", "rootfs")
	require.NoError(t, g.AddNode(image, nil))
	require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "firecracker", Kind: "rootfs", Source: path, Architecture: "amd64", GuestFamily: "linux"}))

	first, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, first[image.String()])
	require.NoError(t, os.WriteFile(path, []byte("second"), 0o644))
	second, err := ResolvePlanArtifactDigests(context.Background(), g)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}
