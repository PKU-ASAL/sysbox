// Package network creates and wires network primitives for sysbox fields:
// network namespaces, Linux bridges, veth pairs, and (later) nftables rules.
//
// All operations go through netlink; we don't shell out to iproute2.
package network

import (
	"fmt"
	"os"
	"runtime"

	"github.com/vishvananda/netns"
)

// CreateNetns creates a new named network namespace at /var/run/netns/<name>.
//
// netns.NewNamed creates AND enters the namespace. We want to leave the
// caller's goroutine in its original namespace, so we lock the OS thread,
// remember the original ns, create the new one, then switch back.
func CreateNetns(name string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer orig.Close()

	ns, err := netns.NewNamed(name)
	if err != nil {
		return fmt.Errorf("create netns %s: %w", name, err)
	}
	ns.Close()

	if err := netns.Set(orig); err != nil {
		return fmt.Errorf("restore netns: %w", err)
	}
	return nil
}

// DeleteNetns removes the named namespace.
func DeleteNetns(name string) error {
	if err := netns.DeleteNamed(name); err != nil {
		return fmt.Errorf("delete netns %s: %w", name, err)
	}
	return nil
}

// NetnsExists reports whether a named namespace exists.
func NetnsExists(name string) bool {
	_, err := os.Stat("/var/run/netns/" + name)
	return err == nil
}
