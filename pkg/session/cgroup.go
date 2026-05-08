package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const cgroupRoot = "/sys/fs/cgroup/sysbox.slice"

// SessionCgroupPath returns the host-side cgroup directory for a session.
func SessionCgroupPath(nodeID, sessionID string) string {
	return filepath.Join(cgroupRoot, nodeID, sessionID)
}

// CreateSessionCgroup creates the session cgroup directory and returns its
// kernel cgroup_id (inode of the directory in cgroupfs).
//
// Requires: cgroup v2 unified hierarchy mounted at /sys/fs/cgroup, host root.
func CreateSessionCgroup(nodeID, sessionID string) (uint64, error) {
	path := SessionCgroupPath(nodeID, sessionID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir session cgroup %s: %w", path, err)
	}
	return cgroupID(path)
}

// MoveProcess writes pid to the session cgroup's cgroup.procs, migrating
// the process (and all its future children) into the session cgroup.
func MoveProcess(nodeID, sessionID string, pid int) error {
	path := filepath.Join(SessionCgroupPath(nodeID, sessionID), "cgroup.procs")
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

// DeleteSessionCgroup removes the session cgroup.
// Must be called after all processes have left the cgroup.
func DeleteSessionCgroup(nodeID, sessionID string) error {
	path := SessionCgroupPath(nodeID, sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session cgroup %s: %w", path, err)
	}
	return nil
}

// EnsureSliceExists creates the sysbox.slice and per-node subdirectory if
// they don't already exist.
func EnsureSliceExists(nodeID string) error {
	return os.MkdirAll(filepath.Join(cgroupRoot, nodeID), 0o755)
}

// cgroupID returns the inode number of the cgroup directory, which is the
// stable cgroup_id reported by the kernel in Tracee events.
func cgroupID(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return st.Ino, nil
}

// ReadCgroupID reads the cgroup_id for an already-existing session cgroup.
func ReadCgroupID(nodeID, sessionID string) (uint64, error) {
	return cgroupID(SessionCgroupPath(nodeID, sessionID))
}

// NodeCgroupIDs returns a map of sessionID → cgroup_id for all active sessions
// under a node.  Used by the sensor to bootstrap the Labeler at startup.
func NodeCgroupIDs(nodeID string) (map[string]uint64, error) {
	dir := filepath.Join(cgroupRoot, nodeID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string]uint64, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		id, err := cgroupID(filepath.Join(dir, sid))
		if err != nil {
			continue
		}
		out[sid] = id
	}
	return out, nil
}

// CgroupIDFromProc reads the cgroup_id of a running process by inspecting
// /proc/<pid>/cgroup and stat-ing the matching directory.
// Returns 0 if the process is not in a sysbox session cgroup.
func CgroupIDFromProc(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroup v2: "0::/some/path"
		if strings.HasPrefix(line, "0::") {
			rel := strings.TrimPrefix(line, "0::")
			rel = strings.TrimSpace(rel)
			abs := filepath.Join("/sys/fs/cgroup", rel)
			return cgroupID(abs)
		}
	}
	return 0, nil
}
