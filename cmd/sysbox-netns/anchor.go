package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func validateAnchorName(name string) error {
	if name == "" || len(name) > 255 || name == "." || name == ".." || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid namespace name")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("invalid namespace name")
		}
	}
	return nil
}

func anchorIdentity(path string) (uint64, uint64, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	kind, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWNET {
		return 0, 0, fmt.Errorf("not a network namespace: %s", path)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), st.Ino, nil
}

// Only the root executor can invoke this entry. It has no setuid privileges,
// accepts no caller-supplied host path, and does not make resource decisions.
func runAnchor(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	if os.Geteuid() != 0 {
		return fail(fmt.Errorf("namespace anchor requires root"))
	}
	if len(args) < 2 {
		return fail(fmt.Errorf("namespace anchor action and name required"))
	}
	local := args[0] == "--anchor-local"
	if local {
		args = args[1:]
	}
	if len(args) < 2 {
		return fail(fmt.Errorf("namespace anchor action and name required"))
	}
	action, name := args[0], args[1]
	if err := validateAnchorName(name); err != nil {
		return fail(err)
	}
	if action != "--anchor-create" && action != "--anchor-delete" {
		return fail(fmt.Errorf("invalid anchor action"))
	}
	if action == "--anchor-create" && len(args) != 2 || action == "--anchor-delete" && len(args) != 4 {
		return fail(fmt.Errorf("invalid anchor arguments"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !local {
		// The helper can run inside a container with host PID visibility. After
		// entering PID 1's mount namespace its image path need not exist. Pin the
		// executing inode, not a path resolved again against the host filesystem.
		self, err := os.Open("/proc/self/exe")
		if err != nil {
			return fail(err)
		}
		defer self.Close()
		cmd := anchorHostCommand(ctx, self, args)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			return fail(err)
		}
		return 0
	}
	for _, ns := range []string{"mnt", "net"} {
		current, err := os.Stat("/proc/self/ns/" + ns)
		if err != nil {
			return fail(err)
		}
		host, err := os.Stat("/proc/1/ns/" + ns)
		if err != nil {
			return fail(err)
		}
		if !os.SameFile(current, host) {
			return fail(fmt.Errorf("anchor is not in host %s namespace", ns))
		}
	}
	lock, err := lockAnchors(ctx)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	path := filepath.Join("/run/netns", name)
	verb := "add"
	if action == "--anchor-delete" {
		dev, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fail(err)
		}
		inode, err := strconv.ParseUint(args[3], 10, 64)
		if err != nil {
			return fail(err)
		}
		actualDev, actualInode, err := anchorIdentity(path)
		if err != nil {
			return fail(err)
		}
		if dev != actualDev || inode != actualInode {
			return fail(fmt.Errorf("namespace identity changed"))
		}
		verb = "delete"
	} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fail(fmt.Errorf("namespace anchor already exists or cannot be inspected"))
	}
	// ip establishes the shared /run/netns mount before publishing the anchor.
	cmd := exec.CommandContext(ctx, "ip", "netns", verb, name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	// The actual mutator retains flock even if its supervising helper dies.
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fail(err)
	}
	if verb == "add" {
		if _, _, err := anchorIdentity(path); err != nil {
			return fail(err)
		}
	}
	return 0
}

// FD 3 is inherited by nsenter and survives its --no-fork exec. The helper is
// static, so executing this pinned file needs no container-root dynamic loader.
func anchorHostCommand(ctx context.Context, self *os.File, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "nsenter", append([]string{"--no-fork", "--mount=/proc/1/ns/mnt", "--net=/proc/1/ns/net", "--", "/proc/self/fd/3", "--anchor-local"}, args...)...)
	cmd.ExtraFiles = []*os.File{self}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

func lockAnchors(ctx context.Context) (*os.File, error) {
	fd, err := unix.Open("/run/sysbox-netns-anchor.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "namespace anchor lock")
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		file.Close()
		return nil, err
	}
	if st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("untrusted namespace anchor lock")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
