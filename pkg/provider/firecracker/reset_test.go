package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestFirecrackerResetCreatesOwnedWritableRootfsAndRetriesIdempotently(t *testing.T) {
	rootfsDir := t.TempDir()
	baselinePath := filepath.Join(t.TempDir(), "base.ext4")
	baselineBytes := []byte("immutable-rootfs")
	require.NoError(t, os.WriteFile(baselinePath, baselineBytes, 0o644))
	kernelPath := filepath.Join(t.TempDir(), "vmlinux")
	require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0o644))
	oldID := "sysbox-old"
	oldDir := filepath.Join(rootfsDir, oldID)
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldDir, "rootfs.ext4"), []byte("mutated"), 0o644))
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: baselinePath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(baselineBytes)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{
		Current:  substrate.NodeHandle{ID: oldID, Provider: &HandleState{VMDir: oldDir, Socket: filepath.Join(oldDir, "firecracker.sock")}},
		Node:     substrate.NodeSpec{Name: "sysbox-web", Image: substrate.ArtifactHandle{ID: baselinePath, Identity: baseline}, ProviderConfig: &Config{Kernel: kernelPath, SSHPass: "super-secret"}},
		Baseline: baseline,
	}
	sub := New(kernelPath, rootfsDir)

	handle, err := sub.PrepareReset(context.Background(), request)
	require.NoError(t, err)
	preparedState := handle.Provider.(*resetHandleState)
	require.Less(t, len(ownedUnixSocketPath(filepath.Join(rootfsDir, preparedState.NewID), preparedState.NewID, "firecracker.sock", "fc")), 108)
	_, err = os.Stat(oldDir)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(context.Background(), handle))
	_, err = os.Stat(oldDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	created, err := sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	state := handle.Provider.(*resetHandleState)
	require.Equal(t, state.NewID, created.ID)
	newRootfs := filepath.Join(state.NewHandle.VMDir, "rootfs.ext4")
	firstInfo, err := os.Stat(newRootfs)
	require.NoError(t, err)
	actualBaseline, err := os.ReadFile(baselinePath)
	require.NoError(t, err)
	require.Equal(t, baselineBytes, actualBaseline)
	_, err = sub.ApplyReset(context.Background(), handle)
	require.NoError(t, err)
	secondInfo, err := os.Stat(newRootfs)
	require.NoError(t, err)
	require.True(t, os.SameFile(firstInfo, secondInfo))
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "super-secret")
	t.Cleanup(func() { _ = os.RemoveAll(state.NewHandle.VMDir) })
}

func TestFirecrackerResetRejectsUnownedVMDirectory(t *testing.T) {
	sub := New("/tmp/vmlinux", t.TempDir())
	err := sub.validateOwnedVMDir("sysbox-web", "/tmp/unrelated")
	require.ErrorContains(t, err, "refuses unowned")
}

func TestFirecrackerResetRejectsLegacyIncompleteProcessAnchor(t *testing.T) {
	rootfsDir := t.TempDir()
	oldID := "sysbox-old"
	oldDir := filepath.Join(rootfsDir, oldID)
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	pidFile := filepath.Join(oldDir, "firecracker.pid")
	require.NoError(t, os.WriteFile(pidFile, []byte("123\n"), 0o644))
	baselinePath := filepath.Join(t.TempDir(), "base.ext4")
	baselineBytes := []byte("baseline")
	require.NoError(t, os.WriteFile(baselinePath, baselineBytes, 0o644))
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: baselinePath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(baselineBytes)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	request := substrate.ResetRequest{Current: substrate.NodeHandle{ID: oldID, Provider: &HandleState{VMDir: oldDir, PIDFile: pidFile}}, Node: substrate.NodeSpec{Name: "web", Image: substrate.ArtifactHandle{ID: baselinePath, Identity: baseline}}, Baseline: baseline}

	_, err := New("/tmp/vmlinux", rootfsDir).PrepareReset(context.Background(), request)
	require.ErrorContains(t, err, "incomplete or mismatched")
}

func TestFirecrackerDestroyResetRetriesAfterOldGenerationIsGone(t *testing.T) {
	rootfsDir := t.TempDir()
	oldID := "sysbox-old"
	oldDir := filepath.Join(rootfsDir, oldID)
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	pidFile := filepath.Join(oldDir, "firecracker.pid")
	anchor := processAnchor{PID: 999999, StartTime: "123", Socket: filepath.Join(oldDir, "firecracker.sock"), VMID: oldID}
	require.NoError(t, writeProcessAnchor(pidFile, anchor))
	request := substrate.ResetRequest{Current: substrate.NodeHandle{ID: oldID, Provider: &HandleState{VMDir: oldDir, PIDFile: pidFile, PID: anchor.PID, PIDStart: anchor.StartTime, Socket: anchor.Socket}}, Node: substrate.NodeSpec{Name: "web"}}
	handle := substrate.ResetHandle{Provider: &resetHandleState{Version: firecrackerResetHandleVersion, OldID: oldID, OldVMDir: oldDir, OldPID: anchor.PID, OldPIDStart: anchor.StartTime, OldSocket: anchor.Socket}, Request: request}
	sub := New("/tmp/vmlinux", rootfsDir)

	require.NoError(t, sub.DestroyReset(context.Background(), handle))
	require.NoError(t, sub.DestroyReset(context.Background(), handle))
}

func TestFirecrackerResetRejectsMutatedBaseline(t *testing.T) {
	rootfsDir := t.TempDir()
	baselinePath := filepath.Join(t.TempDir(), "base.ext4")
	require.NoError(t, os.WriteFile(baselinePath, []byte("changed"), 0o644))
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: baselinePath, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	handle := substrate.ResetHandle{Provider: &resetHandleState{Version: 1, NewID: "new", BaselinePath: baselinePath, BaselineDigest: baseline.Digest}, Request: substrate.ResetRequest{Node: substrate.NodeSpec{Name: "web"}, Baseline: baseline}}

	_, err := New("/tmp/vmlinux", rootfsDir).ApplyReset(context.Background(), handle)
	require.ErrorContains(t, err, "baseline changed")
}

func TestFirecrackerResetNICConfigReplayReplacesLogicalInterface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm_config.json")
	cfg := fcConfig{NetworkInterfaces: []fcNetworkInterface{{IfaceID: "eth0", HostDevName: "tap-old"}}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	require.NoError(t, appendNICtoConfig(path, fcNetworkInterface{IfaceID: "eth0", HostDevName: "tap-new"}))
	updatedRaw, err := os.ReadFile(path)
	require.NoError(t, err)
	var updated fcConfig
	require.NoError(t, json.Unmarshal(updatedRaw, &updated))
	require.Len(t, updated.NetworkInterfaces, 1)
	require.Equal(t, "tap-new", updated.NetworkInterfaces[0].HostDevName)
}
