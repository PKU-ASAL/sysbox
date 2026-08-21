package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/vsockrpc"
)

func TestInjectInitBinaryDoesNotRequireLoopDevicesOrMount(t *testing.T) {
	requireTool(t, "mkfs.ext4")
	requireTool(t, "debugfs")
	image := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(image, nil, 0o600))
	require.NoError(t, os.Truncate(image, 16*1024*1024))
	require.NoError(t, exec.Command("mkfs.ext4", "-F", "-q", image).Run())

	require.NoError(t, injectInitBinary(image))
	out, err := exec.Command("debugfs", "-R", "stat /sysbox-init", image).CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "Mode:  0755")
}

func TestBuildConfigDriveDoesNotRequireLoopDevicesOrMount(t *testing.T) {
	requireTool(t, "mkfs.ext4")
	requireTool(t, "debugfs")
	image := filepath.Join(t.TempDir(), "config.ext4")
	require.NoError(t, buildConfigDrive(image, vsockrpc.VMConfig{Hostname: "worker"}))

	out, err := exec.Command("debugfs", "-R", "cat /config.json", image).CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), `"hostname": "worker"`)
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is unavailable", name)
	}
}
