package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
// The image is populated directly by mke2fs. This deliberately avoids loop
// devices and mounts so concurrent VMs do not consume host-global loop slots.
func buildConfigDrive(outPath string, cfg vsockrpc.VMConfig) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	seedDir, err := os.MkdirTemp("", "sysbox-cfgdrive-")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(seedDir)
	if err := os.WriteFile(filepath.Join(seedDir, "config.json"), data, 0o644); err != nil {
		return fmt.Errorf("write config.json: %w", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	if err := f.Truncate(int64(configDriveSizeMB) * 1024 * 1024); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate %s: %w", outPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", outPath, err)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-d", seedDir, outPath).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %w\n%s", outPath, err, out)
	}
	return nil
}

// injectInitBinary writes directly into the ext4 image with debugfs. It does
// not allocate a loop device or mount the filesystem.
func injectInitBinary(rootfsPath string) error {
	bin, err := initbin.Bytes()
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp("", "sysbox-init-*")
	if err != nil {
		return fmt.Errorf("create temporary sysbox-init: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(bin); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary sysbox-init: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary sysbox-init: %w", err)
	}
	_, _ = exec.Command("debugfs", "-w", "-R", "rm /sysbox-init", rootfsPath).CombinedOutput()
	if out, err := exec.Command("debugfs", "-w", "-R", "write "+temporaryPath+" /sysbox-init", rootfsPath).CombinedOutput(); err != nil {
		return fmt.Errorf("install sysbox-init into rootfs: %w\n%s", err, out)
	}
	if out, err := exec.Command("debugfs", "-w", "-R", "set_inode_field /sysbox-init mode 0100755", rootfsPath).CombinedOutput(); err != nil {
		return fmt.Errorf("make sysbox-init executable: %w\n%s", err, out)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
