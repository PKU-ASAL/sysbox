package guesttask

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type installIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func installFDIdentity(f *os.File) (installIdentity, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	return installIdentity{Device: uint64(st.Dev), Inode: st.Ino}, err
}
func privateInstallDirectory(f *os.File) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&0022 != 0 {
		return fmt.Errorf("installation directory must be supervisor-owned and not writable by others")
	}
	return nil
}
func installChildDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func openInstallRoot(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("installation root must be canonical absolute directory")
	}
	for _, broad := range []string{"/", "/etc", "/usr", "/usr/local", "/var", "/var/lib", "/var/tmp", "/tmp", "/home", "/root", "/run"} {
		if path == broad {
			return nil, fmt.Errorf("installation root is too broad")
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	traversed := "/"
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := installChildDirectory(current, name)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
		traversed = filepath.Join(traversed, name)
		var st unix.Stat_t
		if err := unix.Fstat(int(current.Fd()), &st); err != nil {
			current.Close()
			return nil, err
		}
		stickySystemTemp := st.Uid == 0 && st.Mode&unix.S_ISVTX != 0 && (traversed == "/tmp" || traversed == "/var/tmp")
		if (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !stickySystemTemp) {
			current.Close()
			return nil, fmt.Errorf("installation path ancestor %s is not supervisor-controlled (uid=%d mode=%o supervisor=%d)", traversed, st.Uid, st.Mode&07777, os.Geteuid())
		}
	}
	if err = privateInstallDirectory(current); err != nil {
		current.Close()
		return nil, err
	}
	return current, nil
}
func pathWithin(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }
func (e *Engine) installParent(path string) (*os.File, error) {
	if len(e.AllowedInstallRoots) == 0 {
		return nil, fmt.Errorf("trusted installation roots not configured")
	}
	protected := []string{e.Root, "/etc/sysbox", "/var/lib/sysbox", "/run/sysbox", "/sysbox-init", "/usr/local/bin/sysbox-init", "/usr/local/bin/sysbox-guest-task"}
	if binary, err := os.Executable(); err == nil {
		protected = append(protected, binary)
	}
	for _, root := range protected {
		if root != "" && pathWithin(path, filepath.Clean(root)) {
			return nil, fmt.Errorf("installation cannot overwrite supervisor control files")
		}
	}
	var selected string
	for _, root := range e.AllowedInstallRoots {
		if filepath.Clean(root) != root {
			return nil, ErrConflict
		}
		if path != root && pathWithin(path, root) && len(root) > len(selected) {
			selected = root
		}
	}
	if selected == "" {
		return nil, fmt.Errorf("file target is outside trusted installation roots")
	}
	root, err := openInstallRoot(selected)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(selected, filepath.Dir(path))
	if err != nil {
		root.Close()
		return nil, err
	}
	if rel != "." {
		for _, name := range strings.Split(rel, string(filepath.Separator)) {
			if name == ".." {
				root.Close()
				return nil, ErrConflict
			}
			child, err := installChildDirectory(root, name)
			root.Close()
			if err != nil {
				return nil, err
			}
			root = child
			if err = privateInstallDirectory(root); err != nil {
				root.Close()
				return nil, err
			}
		}
	}
	return root, nil
}
func installEntry(parent *os.File, name string) (installIdentity, bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == unix.ENOENT {
		return installIdentity{}, false, nil
	}
	if err != nil {
		return installIdentity{}, false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return installIdentity{}, false, fmt.Errorf("install target or anchor is not a regular file")
	}
	return installIdentity{Device: uint64(st.Dev), Inode: st.Ino}, true, nil
}
func openInstallFile(parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func verifyInstalledFile(parent *os.File, name string, record FileInstallRecord) error {
	f, err := openInstallFile(parent, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	identity, err := installFDIdentity(f)
	if err != nil {
		return err
	}
	if identity != record.Anchor {
		return ErrConflict
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != record.Request.Size || uint32(info.Mode().Perm()) != record.Request.Mode {
		return ErrConflict
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, record.Request.Size+1))
	if err != nil {
		return err
	}
	if n != record.Request.Size || fmt.Sprintf("%x", hash.Sum(nil)) != record.Request.SHA256 {
		return ErrConflict
	}
	return nil
}
