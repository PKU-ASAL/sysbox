package network

import (
	"fmt"
	"os/exec"

	"github.com/vishvananda/netlink"
)

// CreateTapInNetns creates a TAP device, places it inside the given netns,
// brings it up, and attaches it to the bridge.
//
// Idempotent in three failure-recovery scenarios:
//
//  1. TAP already lives in the target netns (apply re-run after success of
//     this step but failure later) → only re-check bridge enslavement / up.
//  2. TAP exists in the root netns (apply died before the move-into-netns
//     step) → reuse it and just move it across.
//  3. TAP does not exist anywhere → create it in root, then move it in.
//
// This avoids `ioctl(TUNSETIFF): Device or resource busy` when a failed run
// leaves the TAP behind in any netns.
func CreateTapInNetns(tapName, netnsName, bridgeName string) error {
	inTargetNs := linkExistsInNetnsViaNetlink(netnsName, tapName)

	if !inTargetNs {
		// Is the TAP sitting in the root netns from a previous failed apply?
		_, rootErr := netlink.LinkByName(tapName)
		if rootErr != nil {
			// Truly absent — create it in the root netns first.
			if out, err := exec.Command("ip", "tuntap", "add", "dev", tapName, "mode", "tap").CombinedOutput(); err != nil {
				return fmt.Errorf("create tap %s: %w\n%s", tapName, err, out)
			}
		}

		// Move the TAP into the target network netns.
		if out, err := exec.Command("ip", "link", "set", tapName, "netns", netnsName).CombinedOutput(); err != nil {
			return fmt.Errorf("move tap %s to netns %s: %w\n%s", tapName, netnsName, err, out)
		}
	}

	// Inside the target netns: enslave to the bridge (if not already) and up.
	return inNetns(netnsName, func() error {
		link, err := netlink.LinkByName(tapName)
		if err != nil {
			return fmt.Errorf("find tap %s in netns: %w", tapName, err)
		}
		br, err := netlink.LinkByName(bridgeName)
		if err != nil {
			return fmt.Errorf("find bridge %s: %w", bridgeName, err)
		}
		bridge, ok := br.(*netlink.Bridge)
		if !ok {
			return fmt.Errorf("link %s is not a bridge", bridgeName)
		}
		if link.Attrs().MasterIndex != bridge.Attrs().Index {
			if err := netlink.LinkSetMaster(link, bridge); err != nil {
				return fmt.Errorf("attach tap to bridge: %w", err)
			}
		}
		return netlink.LinkSetUp(link)
	})
}

// DeleteTapDevice removes a TAP device from its netns.
func DeleteTapDevice(tapName, netnsName string) error {
	if netnsName == "" {
		return nil
	}
	return inNetns(netnsName, func() error {
		link, err := netlink.LinkByName(tapName)
		if err != nil {
			return nil // already gone
		}
		return netlink.LinkDel(link)
	})
}

// linkExistsInNetnsViaNetlink reports whether a link with the given name
// exists inside the named netns, using netlink (no exec).
func linkExistsInNetnsViaNetlink(nsName, linkName string) bool {
	found := false
	_ = inNetns(nsName, func() error {
		if _, err := netlink.LinkByName(linkName); err == nil {
			found = true
		}
		return nil
	})
	return found
}
