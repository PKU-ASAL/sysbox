package driver_test

import (
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/stretchr/testify/require"
	"testing"
)

// This is the maintained migration inventory: an aggregate metadata snapshot
// must never advertise a provider VM/memory/disk rollback capability. The main
// baseline exposes the interface but none of these implementations supplies it.
func TestBuiltinProvidersDoNotAdvertiseVMSnapshotFromMetadataSupport(t *testing.T) {
	d, err := docker.New()
	require.NoError(t, err)
	defer d.Close()
	for name, provider := range map[string]any{"docker": d, "firecracker": firecracker.New("", t.TempDir()), "libvirt": libvirt.New(t.TempDir())} {
		t.Run(name, func(t *testing.T) {
			_, ok := provider.(driver.Snapshot)
			require.False(t, ok, "provider snapshot capability requires separate implementation and acceptance")
		})
	}
}
