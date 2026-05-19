package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/oslab/sysbox/pkg/provider/firecracker/initbin"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

// configDriveSizeMB is the size of the ext4 config drive in MiB. 4 is plenty
// for a single small JSON file and leaves headroom for future additions.
const configDriveSizeMB = 4

// buildConfigDrive creates a small ext4 image at `outPath` containing
// `/config.json` with the marshalled cfg. Idempotent: existing file is
// truncated and rebuilt.
//
// Requires root because we use `mkfs.ext4` + `mount -o loop`. sysbox apply
// already runs as root, so this matches the project's existing assumptions.
func buildConfigDrive(outPath string, cfg vsockrpc.VMConfig) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err)
	}

	// Allocate sparse file of configDriveSizeMB MiB.
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	if err := f.Truncate(int64(configDriveSizeMB) * 1024 * 1024); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate %s: %w", outPath, err)
	}
	_ = f.Close()

	if out, err := exec.Command("mkfs.ext4", "-F", "-q", outPath).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %w\n%s", outPath, err, out)
	}

	mountDir, err := os.MkdirTemp("", "sysbox-cfgdrive-")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(mountDir)

	// Use explicit losetup so we can reliably detach the loop device on
	// error (defer alone won't run if the process is SIGKILL'd, but at
	// least normal error paths are covered).
	loopDev, err := exec.Command("losetup", "--find", "--show", outPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup %s: %w\n%s", outPath, err, loopDev)
	}
	loopDevStr := strings.TrimSpace(string(loopDev))
	defer func() { _, _ = exec.Command("losetup", "-d", loopDevStr).CombinedOutput() }()

	if out, err := exec.Command("mount", loopDevStr, mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s on %s: %w\n%s", loopDevStr, mountDir, err, out)
	}
	mounted := true
	defer func() {
		if mounted {
			_ = exec.Command("umount", mountDir).Run()
		}
	}()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "config.json"), data, 0644); err != nil {
		return fmt.Errorf("write config.json: %w", err)
	}

	if out, err := exec.Command("umount", mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %w\n%s", mountDir, err, out)
	}
	mounted = false

	return nil
}

// injectInitBinary mounts the per-VM rootfs ext4 image, copies the embedded
// sysbox-init binary to `/sysbox-init` inside the rootfs, and unmounts.
// Idempotent: an existing `/sysbox-init` is overwritten.
func injectInitBinary(rootfsPath string) error {
	bin, err := initbin.Bytes()
	if err != nil {
		return err
	}

	mountDir, err := os.MkdirTemp("", "sysbox-rootfs-")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(mountDir)

	loopDev, err := exec.Command("losetup", "--find", "--show", rootfsPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup %s: %w\n%s", rootfsPath, err, loopDev)
	}
	loopDevStr := strings.TrimSpace(string(loopDev))
	defer func() { _, _ = exec.Command("losetup", "-d", loopDevStr).CombinedOutput() }()

	if out, err := exec.Command("mount", loopDevStr, mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mount rootfs %s: %w\n%s", loopDevStr, err, out)
	}
	mounted := true
	defer func() {
		if mounted {
			_ = exec.Command("umount", mountDir).Run()
		}
	}()

	dst := filepath.Join(mountDir, "sysbox-init")
	if err := writeFileAtomic(dst, bin, 0755); err != nil {
		return fmt.Errorf("install sysbox-init into rootfs: %w", err)
	}

	if out, err := exec.Command("umount", mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %w\n%s", mountDir, err, out)
	}
	mounted = false

	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
