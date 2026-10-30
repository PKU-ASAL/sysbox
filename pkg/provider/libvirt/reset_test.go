package libvirt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLibvirtResetCreatesIdempotentOverlayWithPinnedBackingAndUUID(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	created, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	hs := hsFrom(created)
	state := handle.Provider.(*resetHandleState)
	require.Equal(t, state.NewDomainUUID, created.ID)
	require.FileExists(t, filepath.Join(state.NewVMDir, "base.qcow2"))
	directory, err := os.Stat(state.NewVMDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), directory.Mode().Perm(), "QEMU must traverse the replacement directory even with a restrictive service umask")
	before, err := os.Stat(hs.DiskPath)
	require.NoError(t, err)
	repeated, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, created.ID, repeated.ID)
	after, err := os.Stat(hs.DiskPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	setLiveResetDomain(t, hs, hs.DomainUUID)
	observation, err := sub.ObserveReset(ctx, handle)
	require.NoError(t, err)
	require.True(t, observation.Converged)
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
	restored, err := sub.UnmarshalResetHandle(raw)
	require.NoError(t, err)
	restored.Request = request
	adopted, err := sub.ApplyReset(ctx, restored)
	require.NoError(t, err)
	require.Equal(t, created.ID, adopted.ID)
}

func TestLibvirtResetRejectsUnownedVMDirectory(t *testing.T) {
	err := validateOwnedVMDir("web", "uuid", "/srv/production", "/srv/production/disk.qcow2")
	require.ErrorContains(t, err, "refuses unowned")
}

func TestLibvirtResetRejectsMatchingDirectoryWithoutOwnershipManifest(t *testing.T) {
	vmDir, err := os.MkdirTemp("", "sysbox-lv-web-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(vmDir) })

	err = validateOwnedVMDir("web", "uuid", vmDir, filepath.Join(vmDir, "disk.qcow2"))
	require.ErrorContains(t, err, "ownership")
}

func TestLibvirtCleanupResetIsIdempotentAfterOwnedDirectoryIsGone(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	require.NoError(t, sub.CleanupReset(ctx, handle))
	require.NoError(t, sub.CleanupReset(ctx, handle))
}

func TestLibvirtPrepareResetIsPureAndDestroyRequiresExactUUID(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	oldDir := hsFrom(request.Current).VMDir
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.DirExists(t, oldDir)
	_, err = os.Stat(handle.Provider.(*resetHandleState).NewVMDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(os.Getenv("SYSBOX_RESET_MUTATIONS"))
	require.ErrorIs(t, err, os.ErrNotExist)
	setLiveResetDomain(t, hsFrom(request.Current), "99999999-2222-4333-8444-555555555555")
	t.Setenv("SYSBOX_RESET_LIVE_UUID", request.Current.ID)
	require.Error(t, sub.DestroyReset(ctx, handle))
	require.DirExists(t, oldDir)
	_, err = os.Stat(os.Getenv("SYSBOX_RESET_MUTATIONS"))
	require.ErrorIs(t, err, os.ErrNotExist)
	t.Setenv("SYSBOX_RESET_LIVE_UUID", "")
	require.NoError(t, sub.DestroyReset(ctx, handle))
	_, err = os.Stat(oldDir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func setLiveResetDomain(t *testing.T, hs *HandleState, actualUUID string) {
	t.Helper()
	raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: actualUUID, DiskPath: hs.DiskPath, VCPUs: 1, MemoryMiB: 512, Identity: hs.Identity, ManagedState: hs})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	t.Setenv("SYSBOX_RESET_DOMAIN_XML", path)
	t.Setenv("SYSBOX_RESET_LIVE_UUID", actualUUID)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}
