package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func stopJail(ctx context.Context, j *jailIdentity) error {
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if j.BootID != boot {
		return fmt.Errorf("refusing to signal a jail from another host boot")
	}
	if err = rejectSymlinks(jailCgroup(j)); err != nil {
		return err
	}
	fd, err := unix.Open(jailCgroup(j), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if j.CgroupInode == 0 || st.Ino != j.CgroupInode {
		return fmt.Errorf("jail cgroup identity changed")
	}
	killFD, err := unix.Openat(fd, "cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	_, err = unix.Write(killFD, []byte("1"))
	unix.Close(killFD)
	if err != nil {
		return err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		empty, err := jailCgroupEmpty(j)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("jail process termination not confirmed")
		case <-ticker.C:
		}
	}
}

func jailCgroup(j *jailIdentity) string { return filepath.Join("/sys/fs/cgroup", j.Cgroup) }

func prepareJailCgroup(m *vmManifest) error {
	j := m.Jail
	path := jailCgroup(j)
	if err := rejectSymlinks(path); err != nil {
		return err
	}
	if err := trustedAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs("/sys/fs/cgroup", &fs); err != nil {
		return err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("jailer requires cgroup v2")
	}
	// Delegation is an administrator prerequisite; do not modify ancestor
	// controller settings or invent a writable cgroupfs hierarchy at runtime.
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		return fmt.Errorf("configured jail cgroup parent must exist: %s", filepath.Dir(path))
	}
	if j.CgroupInode != 0 {
		empty, err := jailCgroupEmpty(j)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if !empty {
				return fmt.Errorf("jailer cgroup is already populated")
			}
			// Each launch gets a fresh kernel object. Besides retiring its old
			// accounting, this avoids carrying cgroup.kill's fork sequence into
			// a subsequent CLONE_INTO_CGROUP on affected kernels.
			if err = os.Remove(path); err != nil {
				return err
			}
		}
		j.CgroupInode = 0
		if err = saveManifest(m); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0755); err != nil {
		if !os.IsExist(err) {
			return err
		}
		actual, e := inode(path)
		if e != nil {
			return e
		}

		if j.CgroupInode == 0 || actual != j.CgroupInode {
			return fmt.Errorf("jailer cgroup identity conflict")
		}
	}
	actual, err := inode(path)
	if err != nil {
		return err
	}
	j.CgroupInode = actual
	if err = saveManifest(m); err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(path, "cgroup.kill")); err != nil {
		return fmt.Errorf("jailer requires host cgroup.kill support: %w", err)
	}
	empty, err := jailCgroupEmpty(j)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("jailer cgroup is already populated")
	}
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return err
	}
	var cfg fcConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.MachineConfig.VcpuCount < 1 || cfg.MachineConfig.MemSizeMB < 1 {
		return fmt.Errorf("invalid VM resource limits")
	}
	// VMM overhead gets a fixed allowance beyond guest RAM. CPU and process
	// limits prevent a compromised VMM from consuming the whole host.
	limits := map[string]string{"memory.max": strconv.FormatInt((int64(cfg.MachineConfig.MemSizeMB)+128)<<20, 10), "memory.swap.max": "0", "pids.max": "64", "cpu.max": fmt.Sprintf("%d 100000", int64(cfg.MachineConfig.VcpuCount)*100000)}
	for name, value := range limits {
		if _, err = os.Stat(filepath.Join(path, name)); err != nil {
			return fmt.Errorf("required jail controller %s is not delegated: %w", name, err)
		}
		if err = os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			return fmt.Errorf("required jail resource limit %s: %w", name, err)
		}
	}
	return nil
}

func jailCgroupEmpty(j *jailIdentity) (bool, error) {
	actual, err := inode(jailCgroup(j))
	if err != nil {
		return false, err
	}
	if actual != j.CgroupInode || actual == 0 {
		return false, fmt.Errorf("jailer cgroup inode changed")
	}
	raw, err := os.ReadFile(filepath.Join(jailCgroup(j), "cgroup.events"))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "populated 0" {
			return true, nil
		}
		if line == "populated 1" {
			return false, nil
		}
	}
	return false, fmt.Errorf("jailer cgroup population unknown")
}

func removeJail(m *vmManifest) error {
	j := m.Jail
	if j == nil {
		return nil
	}
	if err := recoverUnlaunchedJailObjects(m); err != nil {
		return err
	}
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if boot != j.BootID {
		if _, err = os.Lstat(jailCgroup(j)); !os.IsNotExist(err) {
			return fmt.Errorf("current kernel has a cgroup at the fenced jail path")
		}
	}
	if j.CgroupInode != 0 {
		empty, err := jailCgroupEmpty(j)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !empty {
			return fmt.Errorf("jail still has running processes")
		}
		if err == nil {
			if err = os.Remove(jailCgroup(j)); err != nil {
				return err
			}
		}
	}
	if err := rejectSymlinks(j.Root); err != nil {
		return err
	}
	if _, err := os.Lstat(j.Root); err == nil {
		actual, e := inode(j.Root)
		if e != nil || j.RootInode == 0 || actual != j.RootInode {
			return fmt.Errorf("jail root identity changed")
		}
		if err = os.RemoveAll(filepath.Dir(j.Root)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	base := filepath.Dir(filepath.Dir(filepath.Dir(j.Root)))
	reservation := filepath.Join(base, ".uids", strconv.FormatUint(uint64(j.UID), 10)+".json")
	raw, err := readRegularFile(reservation)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var value []string
	if json.Unmarshal(raw, &value) != nil || len(value) != 3 || value[0] != m.Handle.VMDir || value[1] != m.VMID || value[2] != j.Root {
		if !j.Reserved && j.RootInode == 0 && j.CgroupInode == 0 {
			return nil
		}
		return fmt.Errorf("jail UID reservation identity conflict")
	}
	if boot == j.BootID {
		if err := revokeJailTapAccess(m); err != nil {
			return err
		}
	}
	return os.Remove(reservation)
}
