package network

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

type VethSpec struct {
	OwnerAlias string
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
//
// Idempotent: if the host-end already exists in the target netns
// (leftover from a previous failed apply), it is reused and bridge
// enslavement is re-asserted instead of returning "file exists".
func CreateVethPair(spec VethSpec) (VethHandle, error) {
	err := inNetns(spec.NetnsName, func() error {
		hostLink, err := netlink.LinkByName(spec.HostEnd)
		if err == nil {
			if spec.OwnerAlias != "" && hostLink.Attrs().Alias != spec.OwnerAlias {
				return fmt.Errorf("veth ownership conflict")
			}
			// Host end exists. Verify the guest end is also present in this
			// netns; otherwise the pair is broken (the peer was previously
			// moved into a container netns that has since gone away). Reset
			// by deleting the orphan and re-creating the pair fresh.
			if _, perr := netlink.LinkByName(spec.GuestEnd); perr != nil {
				_ = netlink.LinkDel(hostLink)
				hostLink = nil
			}
		}
		if hostLink == nil {
			la := netlink.NewLinkAttrs()
			la.Name = spec.HostEnd
			la.Alias = spec.OwnerAlias
			if spec.OwnerAlias != "" {
				index, err := newAttachmentIndex()
				if err != nil {
					return err
				}
				la.Index = index
			}

			veth := &netlink.Veth{
				LinkAttrs: la,
				PeerName:  spec.GuestEnd,
			}
			if err := netlink.LinkAdd(veth); err != nil {
				return fmt.Errorf("add veth pair: %w", err)
			}
			if spec.OwnerAlias != "" {
				hostLink, err = netlink.LinkByIndex(la.Index)
			} else {
				hostLink, err = netlink.LinkByName(spec.HostEnd)
			}
			if err != nil {
				return err
			}
			if hostLink.Attrs().Name != spec.HostEnd || hostLink.Type() != "veth" {
				return fmt.Errorf("created veth identity changed")
			}
			if spec.OwnerAlias != "" {
				if err := netlink.LinkSetAlias(hostLink, spec.OwnerAlias); err != nil {
					return err
				}
			}
		}

		br, err := netlink.LinkByName(spec.BridgeName)
		if err != nil {
			return err
		}
		bridge, ok := br.(*netlink.Bridge)
		if !ok {
			return fmt.Errorf("link %s is not a bridge", spec.BridgeName)
		}
		if hostLink.Attrs().MasterIndex != bridge.Attrs().Index {
			if err := netlink.LinkSetMaster(hostLink, bridge); err != nil {
				return fmt.Errorf("attach host end to bridge: %w", err)
			}
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
