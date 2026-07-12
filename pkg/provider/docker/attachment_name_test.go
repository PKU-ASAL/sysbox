package docker

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLogicalAttachmentsProduceDistinctStableVethNames(t *testing.T) {
	first := vethName("vh", "sysbox-web-internal")
	require.Equal(t, first, vethName("vh", "sysbox-web-internal"))
	require.NotEqual(t, first, vethName("vh", "sysbox-web-uplink"))
	require.LessOrEqual(t, len(first), 15)
}
