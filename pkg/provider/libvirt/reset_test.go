package libvirt

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestLibvirtResetCreatesIdempotentOverlayWithPinnedBackingAndUUID(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "qemu-img"), `#!/bin/sh
if [ "$1" = "create" ]; then touch "$8"; exit 0; fi
if [ "$1" = "info" ]; then printf '{"backing-filename":"%s"}\n' "$SYSBOX_TEST_BASELINE"; exit 0; fi
exit 1
`)
	writeExecutable(t, filepath.Join(bin, "virsh"), `#!/bin/sh
if [ "$1" = "domuuid" ]; then printf '%s\n' "$SYSBOX_TEST_UUID"; exit 0; fi
exit 1
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	baselinePath := filepath.Join(t.TempDir(), "base.qcow2")
	require.NoError(t, os.WriteFile(baselinePath, []byte("immutable"), 0o644))
	t.Setenv("SYSBOX_TEST_BASELINE", baselinePath)
	t.Setenv("SYSBOX_TEST_UUID", "11111111-2222-4333-8444-555555555555")
	vmDir, err := os.MkdirTemp("", "sysbox-lv-web-reset-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(vmDir) })
	baselineBytes := []byte("immutable")
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactQCow2, Source: baselinePath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(baselineBytes)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{Node: substrate.NodeSpec{Name: "web", Image: substrate.ArtifactHandle{ID: baselinePath, Identity: baseline}, ProviderConfig: &Config{VCPUs: 2, Memory: "1024", MachineType: "q35", SSHUser: "root", SSHPass: "super-secret", NetworkInit: substrate.GuestNetworkInitPreconfigured}}, Baseline: baseline}
	handle := substrate.ResetHandle{Provider: &resetHandleState{Version: 1, DomainName: "web", OldDomainUUID: "old-uuid", NewDomainUUID: "11111111-2222-4333-8444-555555555555", NewVMDir: vmDir, BaselinePath: baselinePath, BaselineDigest: baseline.Digest}, Request: request}
	sub := New()

	created, err := sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	require.Equal(t, "11111111-2222-4333-8444-555555555555", created.ID)
	firstInfo, err := os.Stat(filepath.Join(vmDir, "disk.qcow2"))
	require.NoError(t, err)
	_, err = sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	secondInfo, err := os.Stat(filepath.Join(vmDir, "disk.qcow2"))
	require.NoError(t, err)
	require.True(t, os.SameFile(firstInfo, secondInfo))
	observation, err := sub.ObserveReset(context.Background(), handle)
	require.NoError(t, err)
	require.True(t, observation.Converged)
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "super-secret")
}

func TestLibvirtResetRejectsUnownedVMDirectory(t *testing.T) {
	err := validateOwnedVMDir("web", "/srv/production", "/srv/production/disk.qcow2")
	require.ErrorContains(t, err, "refuses unowned")
}

func TestLibvirtPrepareResetIsPureAndDestroyRequiresExactUUID(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "virsh.log")
	writeExecutable(t, filepath.Join(bin, "virsh"), `#!/bin/sh
printf '%s\n' "$*" >> "$SYSBOX_TEST_VIRSH_LOG"
case "$1" in
  dominfo) printf 'Title: sysbox-managed\n' ;;
  domuuid) printf 'old-domain-uuid\n' ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYSBOX_TEST_VIRSH_LOG", logPath)
	oldVMDir, err := os.MkdirTemp("", "sysbox-lv-web-*")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(oldVMDir, "disk.qcow2"), []byte("overlay"), 0o644))
	baselinePath := filepath.Join(t.TempDir(), "base.qcow2")
	baselineBytes := []byte("baseline")
	require.NoError(t, os.WriteFile(baselinePath, baselineBytes, 0o644))
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactQCow2, Source: baselinePath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(baselineBytes)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{Current: substrate.NodeHandle{ID: "old-domain-uuid", Provider: &HandleState{DomainName: "web", DomainUUID: "old-domain-uuid", VMDir: oldVMDir, DiskPath: filepath.Join(oldVMDir, "disk.qcow2")}}, Node: substrate.NodeSpec{Name: "web", Image: substrate.ArtifactHandle{ID: baselinePath, Identity: baseline}, ProviderConfig: &Config{VCPUs: 1, Memory: "512", NetworkInit: substrate.GuestNetworkInitPreconfigured}}, Baseline: baseline}
	sub := New()

	handle, err := sub.PrepareReset(context.Background(), request)
	require.NoError(t, err)
	_, err = os.Stat(oldVMDir)
	require.NoError(t, err)
	state := handle.Provider.(*resetHandleState)
	_, err = os.Stat(state.NewVMDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(logPath)
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, sub.DestroyReset(context.Background(), handle))
	_, err = os.Stat(oldVMDir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}
