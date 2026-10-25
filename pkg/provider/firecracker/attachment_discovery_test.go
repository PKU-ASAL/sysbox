package firecracker

import (
	"context"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedAttachmentDiscoveryRequiresCompleteManifest(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	discovery, ok := any(s).(driver.ManagedAttachmentDiscovery)
	require.True(t, ok)
	_, err := discovery.DiscoverManagedAttachments(context.Background(), h, []driver.AttachmentRequest{{Name: "missing"}})
	require.Error(t, err)
	found, err := discovery.DiscoverManagedAttachments(context.Background(), h, nil)
	require.NoError(t, err)
	require.Empty(t, found)
}
