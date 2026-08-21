package libvirt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestCreateNodeUsesConfiguredHostVisibleWorkdir(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "qemu-img.log")
	writeExecutable(t, filepath.Join(bin, "virsh"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(bin, "qemu-img"), `#!/bin/sh
printf '%s\n' "$*" >> "$SYSBOX_TEST_QEMU_IMG_LOG"
case "$1" in
  convert) touch "$7" ;;
  create) touch "$8" ;;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYSBOX_TEST_QEMU_IMG_LOG", logPath)

	workdir := t.TempDir()
	handle, err := New(workdir).CreateNode(context.Background(), substrate.NodeSpec{
		Name:  "host-visible",
		Image: substrate.ArtifactHandle{ID: "/images/base.qcow2"},
		ProviderConfig: &Config{
			VCPUs: 1, Memory: "512", SSHUser: "ubuntu",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, New(workdir).DestroyNode(context.Background(), handle)) })

	state := hsFrom(handle)
	relative, err := filepath.Rel(workdir, state.VMDir)
	require.NoError(t, err)
	require.NotEqual(t, "..", relative)
	require.False(t, filepath.IsAbs(relative) && relative != ".")
	require.FileExists(t, state.DiskPath)
	require.FileExists(t, filepath.Join(state.VMDir, "base.qcow2"))
	commands, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(commands), "convert -f qcow2 -O qcow2 /images/base.qcow2 ")
	require.True(t, strings.Contains(string(commands), "create -f qcow2 -b "+filepath.Join(state.VMDir, "base.qcow2")+" -F qcow2 "))
}

func TestCreateNodeGeneratesEphemeralSSHKeyForCloudInit(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is unavailable")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "virsh"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(bin, "qemu-img"), `#!/bin/sh
case "$1" in
  convert) touch "$7" ;;
  create) touch "$8" ;;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	workdir := t.TempDir()
	handle, err := New(workdir).CreateNode(context.Background(), substrate.NodeSpec{
		Name:  "cloud-init-key",
		Image: substrate.ArtifactHandle{ID: "/images/base.qcow2"},
		ProviderConfig: &Config{
			VCPUs: 1, Memory: "512", SSHUser: "ubuntu", NetworkInit: substrate.GuestNetworkInitCloudInit,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, New(workdir).DestroyNode(context.Background(), handle)) })

	state := hsFrom(handle)
	require.NotEmpty(t, state.SSHAuthorizedKey)
	require.Contains(t, state.SSHAuthorizedKey, "ssh-ed25519 ")
	require.FileExists(t, state.SSHKey)
	info, err := os.Stat(state.SSHKey)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Equal(t, state.VMDir, filepath.Dir(state.SSHKey))
}
