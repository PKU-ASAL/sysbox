package libvirt

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func managedResetFixture(t *testing.T) (*Substrate, context.Context, substrate.ResetRequest) {
	t.Helper()
	bin := t.TempDir()
	root := t.TempDir()
	baseline := filepath.Join(t.TempDir(), "base.qcow2")
	data := []byte("immutable base")
	require.NoError(t, os.WriteFile(baseline, data, 0o644))
	writeExecutable(t, filepath.Join(bin, "virsh"), `#!/bin/sh
case "$1" in
 list) if [ -n "$SYSBOX_RESET_LIVE_UUID" ]; then if [ "$3" = "--uuid" ]; then printf "%s\n" "$SYSBOX_RESET_LIVE_UUID"; else printf "web\n"; fi; fi;;
 dumpxml) cat "$SYSBOX_RESET_DOMAIN_XML";;
 domuuid) printf "%s\n" "$SYSBOX_RESET_LIVE_UUID";;
 domstate) printf "shut off\n";;
 destroy|undefine) printf "%s\n" "$*" >> "$SYSBOX_RESET_MUTATIONS";;
esac
`)
	writeExecutable(t, filepath.Join(bin, "qemu-img"), `#!/bin/sh
case "$1" in
 convert) touch "$7";;
 create) touch "$8";;
 info) printf '{"backing-filename":"%s"}\n' "$SYSBOX_TEST_STAGED_BASE";;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYSBOX_RESET_LIVE_UUID", "")
	t.Setenv("SYSBOX_RESET_MUTATIONS", filepath.Join(t.TempDir(), "mutations"))
	sub := New(root)
	old := managedOperation()
	identity := substrate.ArtifactIdentity{Kind: substrate.ArtifactQCow2, Source: baseline, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	spec := substrate.NodeSpec{Name: "web", Image: substrate.ArtifactHandle{ID: baseline, Identity: identity}, ProviderConfig: &Config{VCPUs: 1, Memory: "512", SSHUser: "root", SSHPass: "secret", NetworkInit: substrate.GuestNetworkInitPreconfigured}}
	current, err := sub.CreateNode(substrate.WithOperationIdentity(context.Background(), old), spec)
	require.NoError(t, err)
	target := old
	target.Generation++
	target.ActionID = "reset"
	ctx := substrate.WithOperationIdentity(context.Background(), target)
	dir, err := sub.managedVMDir(target, "web")
	require.NoError(t, err)
	t.Setenv("SYSBOX_TEST_STAGED_BASE", filepath.Join(dir, "base.qcow2"))
	return sub, ctx, substrate.ResetRequest{Current: current, Node: spec, Baseline: identity}
}

func TestLibvirtResetPublishesRecoverableTargetGeneration(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	id, _ := substrate.OperationIdentityFromContext(ctx)
	expectedDir, err := sub.managedVMDir(id, "web")
	require.NoError(t, err)
	require.Equal(t, expectedDir, handle.Provider.(*resetHandleState).NewVMDir)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	created, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, id.Generation, created.Generation)
	require.Equal(t, &id, hsFrom(created).Identity)
	recovered, err := sub.DiscoverManagedNode(ctx, id, "web")
	require.NoError(t, err)
	require.Equal(t, created.ID, recovered.ID)
	before, err := os.Stat(hsFrom(created).DiskPath)
	require.NoError(t, err)
	repeated, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, created.ID, repeated.ID)
	after, err := os.Stat(hsFrom(created).DiskPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	raw, err := sub.MarshalResetHandle(handle)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestLibvirtResetRejectsForeignReplacementUUIDAndOwner(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	require.NoError(t, sub.DestroyReset(ctx, handle))
	created, err := sub.ApplyReset(ctx, handle)
	require.NoError(t, err)
	hs := hsFrom(created)
	setLiveResetDomain(t, hs, "99999999-2222-4333-8444-555555555555")
	_, err = sub.ApplyReset(ctx, handle)
	require.Error(t, err)
	foreign := *hs
	identity := *hs.Identity
	identity.OwnerUID = "another-owner"
	foreign.Identity = &identity
	setLiveResetDomain(t, &foreign, hs.DomainUUID)
	_, err = sub.ApplyReset(ctx, handle)
	require.Error(t, err)
	_, err = sub.ObserveReset(ctx, handle)
	require.Error(t, err)
}

func TestLibvirtResetRejectsTamperedTargetPathBeforeFilesystemMutation(t *testing.T) {
	sub, ctx, request := managedResetFixture(t)
	handle, err := sub.PrepareReset(ctx, request)
	require.NoError(t, err)
	foreign := filepath.Join(t.TempDir(), "foreign")
	handle.Provider.(*resetHandleState).NewVMDir = foreign
	_, err = sub.ApplyReset(ctx, handle)
	require.Error(t, err)
	_, err = os.Stat(foreign)
	require.ErrorIs(t, err, os.ErrNotExist)
}
