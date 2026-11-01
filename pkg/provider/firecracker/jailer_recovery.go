package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Before launch, an empty root-owned object at the already-reserved exact path
// can be recovered after mkdir succeeded but manifest publication failed.
func recoverUnlaunchedJailObjects(m *vmManifest) error {
	j := m.Jail
	if j == nil || m.Process.PID != 0 || m.Phase == "starting" {
		return nil
	}
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if j.BootID != boot {
		return nil
	}
	changed := false
	for _, path := range []string{j.Root, jailCgroup(j)} {
		isRoot := path == j.Root
		if isRoot && j.RootInode != 0 || !isRoot && j.CgroupInode != 0 {
			continue
		}
		if err := rejectSymlinks(path); err != nil {
			return err
		}
		if err := trustedAncestors(filepath.Dir(path)); err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.IsDir() || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unrecorded jail object is not controlled by root")
		}
		if isRoot {
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("unrecorded jail root is not empty")
			}
			j.RootInode = stat.Ino
		} else {
			j.CgroupInode = stat.Ino
			empty, err := jailCgroupEmpty(j)
			if err != nil || !empty {
				j.CgroupInode = 0
				return fmt.Errorf("unrecorded jail cgroup is not empty")
			}
		}
		changed = true
	}
	if changed {
		return saveManifest(m)
	}
	return nil
}

func (s *Substrate) jailResidueAbsent(id string) error {
	if s.jailBase == "" {
		return nil
	}
	root := filepath.Join(s.jailBase, filepath.Base(s.firecrackerBin), id)
	if err := rejectSymlinks(root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		return fmt.Errorf("jail generation residue remains: %s", root)
	}
	cgroup := filepath.Join("/sys/fs/cgroup", s.cgroupParent, id)
	if _, err := os.Lstat(cgroup); !os.IsNotExist(err) {
		return fmt.Errorf("jail cgroup residue remains: %s", cgroup)
	}
	entries, err := os.ReadDir(filepath.Join(s.jailBase, ".uids"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readRegularFile(filepath.Join(s.jailBase, ".uids", entry.Name()))
		if err != nil {
			return err
		}
		var reservation []string
		if json.Unmarshal(raw, &reservation) != nil || len(reservation) != 3 {
			return fmt.Errorf("invalid jail UID reservation")
		}
		if reservation[1] == id {
			return fmt.Errorf("jail UID reservation remains")
		}
	}
	return nil
}
