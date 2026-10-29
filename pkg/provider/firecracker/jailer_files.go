package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func prepareJailFiles(m *vmManifest) error {
	j := m.Jail
	if j.Root != m.Handle.JailRoot {
		return fmt.Errorf("jail root differs from admitted handle")
	}
	if err := ensureRootDirectory(filepath.Dir(j.Root), 0700); err != nil {
		return err
	}
	if err := rejectSymlinks(j.Root); err != nil {
		return err
	}
	if st, err := os.Lstat(j.Root); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("jail root is not a directory")
		}
		if j.RootInode == 0 {
			return fmt.Errorf("unrecorded jail root requires inspection")
		}

		if actual, err := inode(j.Root); err != nil || actual != j.RootInode {
			return fmt.Errorf("jail root inode changed")
		}
		// The cgroup was proved empty before entering this function. Remove
		// previous device nodes and pidfiles before jailer recreates its runtime.
		entries, err := os.ReadDir(j.Root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = os.RemoveAll(filepath.Join(j.Root, entry.Name())); err != nil {
				return err
			}
		}
	} else if os.IsNotExist(err) {
		if j.RootInode != 0 {
			return fmt.Errorf("recorded jail root is missing")
		}
		if err = os.Mkdir(j.Root, 0700); err != nil {
			return err
		}
		j.RootInode, err = inode(j.Root)
		if err != nil {
			return err
		}
		if err = saveManifest(m); err != nil {
			return err
		}
	} else {
		return err
	}
	// The jail's parent and manifest remain root-only even though jailer changes
	// ownership of the jail root itself. Every staged source is a fixed file.
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return err
	}
	var cfg fcConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.BootSource.KernelImagePath == "" || len(cfg.Drives) < 1 || len(cfg.Drives) > 2 {
		return fmt.Errorf("incomplete jailed VM boot configuration")
	}
	if err = stageJailCopy(cfg.BootSource.KernelImagePath, filepath.Join(j.Root, "vmlinux"), 0444); err != nil {
		return err
	}
	cfg.BootSource.KernelImagePath = "/vmlinux"
	for i := range cfg.Drives {
		drive := &cfg.Drives[i]
		name := ""
		switch drive.DriveID {
		case "rootfs":
			name = "rootfs.ext4"
		case "config":
			name = "config.ext4"
		default:
			return fmt.Errorf("unsupported jailed drive")
		}
		if drive.PathOnHost != filepath.Join(m.Handle.VMDir, name) {
			return fmt.Errorf("jailed drive escapes owned generation")
		}
		path := filepath.Join(j.Root, name)
		if err = rejectSymlinks(path); err != nil {
			return err
		}
		if err = rejectSymlinks(drive.PathOnHost); err != nil {
			return err
		}
		if err = os.Link(drive.PathOnHost, path); err != nil {
			if !os.IsExist(err) {
				return err
			}
			source, e1 := os.Stat(drive.PathOnHost)
			dest, e2 := os.Stat(path)
			if e1 != nil || e2 != nil || !os.SameFile(source, dest) {
				return fmt.Errorf("jailed drive identity conflict")
			}
		}
		if err = os.Chown(path, int(j.UID), int(j.GID)); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if drive.IsReadOnly {
			mode = 0400
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
		drive.PathOnHost = "/" + name
	}
	if cfg.Vsock != nil {
		cfg.Vsock.UDSPath = "/run/vsock.sock"
	}
	raw, err = json.Marshal(cfg)
	if err != nil {
		return err
	}
	config := filepath.Join(j.Root, "vm_config.json")
	if err = rejectSymlinks(config); err != nil {
		return err
	}
	if err = atomicWriteFile(config, raw, 0444); err != nil {
		return err
	}
	return nil
}

func stageJailCopy(source, dest string, mode os.FileMode) error {
	if err := rejectSymlinks(source); err != nil {
		return err
	}
	if err := rejectSymlinks(dest); err != nil {
		return err
	}
	st, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("jail input must be a regular file")
	}
	if st, err := os.Lstat(dest); err == nil {
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || !st.Mode().IsRegular() {
			return fmt.Errorf("unsafe existing jail input")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = copyFile(source, dest); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}
