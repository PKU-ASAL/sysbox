// Package network creates and wires network primitives for sysbox fields.
package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// CreateNetns reuses only a real network namespace, never a stale anchor file.
func CreateNetns(name string) error {
	if _, err := os.Lstat("/var/run/netns/" + name); err == nil {
		ns, err := openNamedNetwork(name)
		if err != nil {
			return err
		}
		return ns.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	ns, err := createExclusiveNetns(name)
	if err != nil {
		return err
	}
	return ns.Close()
}

func anchorCommand(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sysbox-netns", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("host namespace anchor: %w: %s", err, out)
	}
	return nil
}

func openNamedNetwork(name string) (netns.NsHandle, error) {
	// Names are identifiers, not paths, even at read-only entry points.
	if name == "" || name == "." || name == ".." {
		return netns.None(), fmt.Errorf("invalid namespace name")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return netns.None(), fmt.Errorf("invalid namespace name")
		}
	}
	fd, err := unix.Open("/var/run/netns/"+name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return netns.None(), err
	}
	kind, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWNET {
		unix.Close(fd)
		return netns.None(), fmt.Errorf("invalid network namespace anchor %s", name)
	}
	return netns.NsHandle(fd), nil
}

func createExclusiveNetns(name string) (netns.NsHandle, error) {
	if err := anchorCommand("--anchor-create", name); err != nil {
		return netns.None(), err
	}
	// Failure to see the host mount is uncertainty, never a local fallback.
	return openNamedNetwork(name)
}

// DeleteNetns preserves the exact namespace identity across the helper boundary.
// The provider's ownership checks and host-wide lock remain authoritative.
func DeleteNetns(name string) error {
	ns, err := openNamedNetwork(name)
	if err != nil {
		return err
	}
	defer ns.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(ns), &st); err != nil {
		return err
	}
	return anchorCommand("--anchor-delete", name, fmt.Sprint(uint64(st.Dev)), fmt.Sprint(st.Ino))
}

func NetnsExists(name string) bool {
	ns, err := openNamedNetwork(name)
	if err != nil {
		return false
	}
	ns.Close()
	return true
}
