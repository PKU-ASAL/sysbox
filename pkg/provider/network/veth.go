package network

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

type VethSpec struct {
	HostEnd    string
	GuestEnd   string
	NetnsName  string
	BridgeName string
	// GuestIP and Gateway are NOT configured here; they are applied later by
	// substrate.AttachNIC once the guest-end is moved into the container netns.
}

type VethHandle struct {
	HostEnd   string
	GuestEnd  string
	NetnsName string
}

// CreateVethPair creates a veth pair in the netns, attaches the host end
// to the bridge, and prepares the guest end (caller moves it into the
// container netns via substrate.AttachNIC).
func CreateVethPair(spec VethSpec) (VethHandle, error) {
	err := inNetns(spec.NetnsName, func() error {
		la := netlink.NewLinkAttrs()
		la.Name = spec.HostEnd

		veth := &netlink.Veth{
			LinkAttrs: la,
			PeerName:  spec.GuestEnd,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("add veth pair: %w", err)
		}

		hostLink, err := netlink.LinkByName(spec.HostEnd)
		if err != nil {
			return err
		}
		br, err := netlink.LinkByName(spec.BridgeName)
		if err != nil {
			return err
		}
		bridge, ok := br.(*netlink.Bridge)
		if !ok {
			return fmt.Errorf("link %s is not a bridge", spec.BridgeName)
		}
		if err := netlink.LinkSetMaster(hostLink, bridge); err != nil {
			return fmt.Errorf("attach host end to bridge: %w", err)
		}
		if err := netlink.LinkSetUp(hostLink); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return VethHandle{}, err
	}

	return VethHandle{
		HostEnd:   spec.HostEnd,
		GuestEnd:  spec.GuestEnd,
		NetnsName: spec.NetnsName,
	}, nil
}

// DeleteVethPair removes the pair. Deleting either end deletes both.
func DeleteVethPair(h VethHandle) error {
	if h.NetnsName == "" {
		// host end may already have moved; nothing to delete here.
		return nil
	}
	return inNetns(h.NetnsName, func() error {
		link, err := netlink.LinkByName(h.HostEnd)
		if err != nil {
			return nil
		}
		return netlink.LinkDel(link)
	})
}
