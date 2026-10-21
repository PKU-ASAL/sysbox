package libvirt

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFencedGCInspectionIsLocalReadOnly(t *testing.T) {
	virshFixture(t, `#!/bin/sh
case "$1" in
 uri) echo qemu:///system;;
 list) exit 0;;
 *) echo unexpected-mutation >&2;exit 9;;
esac
`)
	presence, err := New(t.TempDir()).InspectFencedGC(context.Background(), "11111111-2222-4333-8444-555555555555", "owner", "resource", 1)
	require.NoError(t, err)
	require.Equal(t, "absent", presence)
}
