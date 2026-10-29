package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/sys/unix"
)

// jailIdentity is host-owned metadata, never stored inside the writable jail.
type jailIdentity struct {
	Reserved         bool   `json:"reserved"`
	Root             string `json:"root"`
	UID              uint32 `json:"uid"`
	GID              uint32 `json:"gid"`
	Cgroup           string `json:"cgroup"`
	CgroupInode      uint64 `json:"cgroup_inode"`
	RootInode        uint64 `json:"root_inode"`
	Executable       string `json:"executable"`
	ExecutableDigest string `json:"executable_digest"`
	JailerDigest     string `json:"jailer_digest"`
	Jailer           string `json:"jailer"`
	BootID           string `json:"boot_id"`
}

func jailerArguments(m *vmManifest, j jailIdentity) ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	if len(m.Handle.Attachments) > 0 && m.Handle.NetnsName == "" {
		return nil, fmt.Errorf("firecracker network attachments require a named network namespace")
	}
	if !validIdentityPart(m.VMID) || j.UID == 0 || j.GID != j.UID || !filepath.IsAbs(j.Root) || !filepath.IsAbs(j.Executable) || filepath.Base(j.Root) != "root" || filepath.Base(filepath.Dir(j.Root)) != m.VMID || filepath.Base(filepath.Dir(filepath.Dir(j.Root))) != filepath.Base(j.Executable) {
		return nil, fmt.Errorf("invalid jail identity")
	}
	if j.Cgroup == "" || filepath.IsAbs(j.Cgroup) || filepath.Clean(j.Cgroup) != j.Cgroup || j.Cgroup == ".." || strings.HasPrefix(j.Cgroup, "../") {
		return nil, fmt.Errorf("invalid jail cgroup")
	}
	if filepath.Base(j.Cgroup) != m.VMID {
		return nil, fmt.Errorf("jail cgroup must bind the VM identity")
	}
	args := []string{"--exec-file", j.Executable, "--id", m.VMID, "--uid", strconv.FormatUint(uint64(j.UID), 10), "--gid", strconv.FormatUint(uint64(j.GID), 10), "--chroot-base-dir", filepath.Dir(filepath.Dir(filepath.Dir(j.Root))), "--cgroup-version", "2", "--parent-cgroup", j.Cgroup, "--resource-limit", "no-file=256"}
	if m.Handle.NetnsName != "" {
		if !validIdentityPart(m.Handle.NetnsName) {
			return nil, fmt.Errorf("invalid jail network namespace")
		}
		args = append(args, "--netns", filepath.Join("/run/netns", m.Handle.NetnsName))
	}
	return append(args, "--", "--config-file", "/vm_config.json", "--api-sock", "/run/firecracker.sock"), nil
}

func trustedExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("trusted executable requires absolute path: %q", path)
	}
	if err := rejectSymlinks(path); err != nil {
		return "", err
	}
	if err := trustedAncestors(filepath.Dir(path)); err != nil {
		return "", err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 || stat.Uid != 0 {
		return "", fmt.Errorf("executable must be root-owned and not writable by other users: %s", path)
	}
	return path, nil
}

func (s *Substrate) newJailIdentity(m *vmManifest) (jailIdentity, error) {
	executable, err := trustedExecutable(s.firecrackerBin)
	if err != nil {
		return jailIdentity{}, err
	}
	jailer, err := trustedExecutable(s.jailerBin)
	if err != nil {
		return jailIdentity{}, err
	}
	if err = verifyJailerVersions(executable, jailer); err != nil {
		return jailIdentity{}, err
	}
	if !filepath.IsAbs(s.jailBase) || filepath.Clean(s.jailBase) != s.jailBase {
		return jailIdentity{}, fmt.Errorf("absolute jail root required")
	}
	boot, err := currentBootID()
	if err != nil {
		return jailIdentity{}, err
	}
	sum := sha256.Sum256([]byte(m.Handle.VMDir + "\x00" + m.VMID))
	uid := uint32(200000) + binary.BigEndian.Uint32(sum[:4])%65536
	j := jailIdentity{Root: filepath.Join(s.jailBase, filepath.Base(executable), m.VMID, "root"), UID: uid, GID: uid, Cgroup: filepath.Join(s.cgroupParent, m.VMID), Executable: executable, Jailer: jailer, BootID: boot}
	if len(filepath.Join(j.Root, "run/firecracker.sock")) > 100 {
		return j, fmt.Errorf("jail root is too long for Unix sockets")
	}
	j.ExecutableDigest, err = digestFile(executable)
	if err != nil {
		return j, err
	}
	j.JailerDigest, err = digestFile(jailer)
	if err != nil {
		return j, err
	}
	_, err = jailerArguments(m, j)
	return j, err
}

func verifyJailerVersions(executable, jailer string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for path, prefix := range map[string]string{executable: "Firecracker v1.15.1", jailer: "Jailer v1.15.1"} {
		raw, err := exec.CommandContext(ctx, path, "--version").Output()
		if err != nil {
			return fmt.Errorf("verify %s version: %w", path, err)
		}
		if strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]) != prefix {
			return fmt.Errorf("supported jailed runtime requires %s", prefix)
		}
	}
	return nil
}

func ensureRootDirectory(path string, mode os.FileMode) error {
	if err := trustedAncestors(path); err != nil {
		return err
	}
	if err := rejectSymlinks(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("jail control directory must be root-owned: %s", path)
	}
	return nil
}

func trustedAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("absolute canonical host path required")
	}
	for current := path; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			stat, ok := st.Sys().(*syscall.Stat_t)
			if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("untrusted host path ancestor %s", current)
			}
		}
		if current == "/" {
			return nil
		}
	}
}

func reserveJailUID(ctx context.Context, m *vmManifest, j jailIdentity) error {
	base := filepath.Dir(filepath.Dir(filepath.Dir(j.Root)))
	path := filepath.Join(base, ".uids")
	if err := ensureRootDirectory(path, 0700); err != nil {
		return err
	}
	l := flock.New(filepath.Join(path, ".lock"))
	defer l.Close()
	ok, err := l.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return ctx.Err()
	}
	path = filepath.Join(path, strconv.FormatUint(uint64(j.UID), 10)+".json")
	want := []string{m.Handle.VMDir, m.VMID, j.Root}
	if raw, err := readRegularFile(path); err == nil {
		var existing []string
		if json.Unmarshal(raw, &existing) != nil || strings.Join(existing, "\x00") != strings.Join(want, "\x00") {
			return fmt.Errorf("jail UID collision; existing reservation retained")
		}
		if !m.Jail.Reserved {
			m.Jail.Reserved = true
			return saveManifest(m)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, _ := json.Marshal(want)
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	m.Jail.Reserved = true
	return saveManifest(m)
}

func inode(path string) (uint64, error) {
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	return st.Ino, err
}

func (s *Substrate) prepareJailer(ctx context.Context, m *vmManifest) (*exec.Cmd, error) {
	if m == nil || len(m.Handle.Attachments) > 0 && m.Handle.NetnsName == "" {
		return nil, fmt.Errorf("firecracker network attachments require a named network namespace")
	}
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("Firecracker jailer requires the root host supervisor")
	}
	if m.Handle.JailRoot == "" {
		return nil, fmt.Errorf("manifest has no jail layout; rebuild required")
	}
	if err := trustedAncestors(m.Handle.VMDir); err != nil {
		return nil, err
	}
	if m.Jail == nil {
		j, err := s.newJailIdentity(m)
		if err != nil {
			return nil, err
		}
		m.Jail = &j
		if err = saveManifest(m); err != nil {
			return nil, err
		}
	}
	j := m.Jail
	if j.Executable != s.firecrackerBin || j.Jailer != s.jailerBin || j.Root != filepath.Join(s.jailBase, filepath.Base(s.firecrackerBin), m.VMID, "root") || j.Cgroup != filepath.Join(s.cgroupParent, m.VMID) {
		return nil, fmt.Errorf("jail differs from current host configuration; rebuild required")
	}
	if _, err := trustedExecutable(j.Executable); err != nil {
		return nil, err
	}
	if _, err := trustedExecutable(j.Jailer); err != nil {
		return nil, err
	}
	for path, expected := range map[string]string{j.Executable: j.ExecutableDigest, j.Jailer: j.JailerDigest} {
		actual, err := digestFile(path)
		if err != nil || actual != expected {
			return nil, fmt.Errorf("jailed runtime binary changed; rebuild required")
		}
	}
	boot, err := currentBootID()
	if err != nil || boot != j.BootID {
		return nil, fmt.Errorf("jail belongs to a different host boot; explicit recovery required")
	}
	if err = reserveJailUID(ctx, m, *j); err != nil {
		return nil, err
	}
	if err = recoverUnlaunchedJailObjects(m); err != nil {
		return nil, err
	}
	if err = prepareJailCgroup(m); err != nil {
		return nil, err
	}
	if err = prepareJailFiles(m); err != nil {
		return nil, err
	}
	if err = saveManifest(m); err != nil {
		return nil, err
	}
	args, err := jailerArguments(m, *j)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(j.Jailer, args...)
	// A PID namespace without daemonization preserves the host PID across the
	// jailer's exec. Isolated IPC/UTS and, for unconnected VMs, network as well.
	flags := uintptr(unix.CLONE_NEWPID | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS)
	if m.Handle.NetnsName == "" {
		flags |= unix.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Cloneflags: flags}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	return cmd, nil
}
