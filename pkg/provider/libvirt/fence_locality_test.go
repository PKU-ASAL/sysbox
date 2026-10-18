package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFenceLocalityRequiresSystemURI(t *testing.T) {
	for _, uri := range []string{"qemu:///system", "qemu+ssh://other/system"} {
		t.Run(uri, func(t *testing.T) {
			virshFixture(t, "#!/bin/sh\nprintf '%s\\n' '"+uri+"'\n")
			scope, err := New().HostFenceScope(context.Background())
			if uri == "qemu:///system" {
				require.NoError(t, err)
				require.Equal(t, "local-only", scope)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestFenceDeleteAbsenceRejectsRetainedManagedDisk(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "vm")
	require.NoError(t, os.Mkdir(dir, 0700))
	virshFixture(t, "#!/bin/sh\ncase \"$1\" in uri) printf 'qemu:///system\\n';;list) exit 0;;esac\n")
	s := New(root)
	h := substrate.NodeHandle{ID: "11111111-1111-4111-8111-111111111111", Provider: &HandleState{DomainUUID: "11111111-1111-4111-8111-111111111111", VMDir: dir}}
	require.Error(t, s.HostFenceDeletionAbsent(context.Background(), h))
	require.NoError(t, os.Remove(dir))
	require.NoError(t, s.HostFenceDeletionAbsent(context.Background(), h))
}
