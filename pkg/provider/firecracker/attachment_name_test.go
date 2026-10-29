package firecracker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogicalAttachmentsProduceDistinctStableTapNames(t *testing.T) {
	first := attachmentTapName("sysbox-web", "internal")
	require.Equal(t, first, attachmentTapName("sysbox-web", "internal"))
	require.NotEqual(t, first, attachmentTapName("sysbox-web", "uplink"))
	require.LessOrEqual(t, len(first), 15)
}
