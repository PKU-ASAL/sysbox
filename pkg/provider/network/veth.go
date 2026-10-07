package network

import (
	"context"
	"fmt"
	"github.com/vishvananda/netlink"
)

type VethSpec struct {
	OwnerAlias string
	HostEnd    string
	GuestEnd   string
}

type VethHandle struct {
	HostIndex  int
	GuestIndex int
	OwnerAlias string
	HostEnd    string
	GuestEnd   string
	NetnsName  string
}

func CreateVethPair(ctx context.Context, target BridgeTarget, spec VethSpec) (VethHandle, error) {
	bridge, err := OpenOwnedBridge(ctx, target)
	if err != nil {
		return VethHandle{}, err
	}
	defer bridge.Close()
	return bridge.CreateVethPair(ctx, spec)
}

// CreateVethPair uses the pinned target handle for both validation and effects.
// An unavailable/moved peer is not evidence that a pair may be deleted.
func (b *OwnedBridge) CreateVethPair(ctx context.Context, spec VethSpec) (VethHandle, error) {
	if spec.OwnerAlias == "" {
		return VethHandle{}, fmt.Errorf("veth ownership alias required")
	}
	if err := b.Check(ctx); err != nil {
		return VethHandle{}, err
	}
	host, err := b.handle.LinkByName(spec.HostEnd)
	if err != nil && !missingLink(err) {
		return VethHandle{}, err
	}
	if err == nil {
		if host.Type() != "veth" || host.Attrs().Alias != spec.OwnerAlias {
			return VethHandle{}, fmt.Errorf("veth ownership conflict")
		}
		peer, err := b.handle.LinkByName(spec.GuestEnd)
		if err != nil {
			return VethHandle{}, fmt.Errorf("veth peer unavailable; refusing destructive retry: %w", err)
		}
		if peer.Type() != "veth" || peer.Attrs().ParentIndex != host.Attrs().Index {
			return VethHandle{}, fmt.Errorf("veth peer identity conflict")
		}
	} else {
		index, err := newAttachmentIndex()
		if err != nil {
			return VethHandle{}, err
		}
		link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: spec.HostEnd, Index: index}, PeerName: spec.GuestEnd}
		if err := b.handle.LinkAdd(link); err != nil {
			return VethHandle{}, fmt.Errorf("add veth pair: %w", err)
		}
		host, err = b.handle.LinkByIndex(index)
		if err != nil {
			return VethHandle{}, err
		}
		if host.Attrs().Name != spec.HostEnd || host.Type() != "veth" {
			return VethHandle{}, fmt.Errorf("created veth identity changed")
		}
		if err := b.handle.LinkSetAlias(host, spec.OwnerAlias); err != nil {
			return VethHandle{}, err
		}
	}
	if err := b.connect(ctx, host); err != nil {
		return VethHandle{}, err
	}
	peer, err := b.handle.LinkByName(spec.GuestEnd)
	if err != nil {
		return VethHandle{}, err
	}
	if peer.Type() != "veth" || peer.Attrs().ParentIndex != host.Attrs().Index {
		return VethHandle{}, fmt.Errorf("veth peer identity conflict")
	}
	if err := b.handle.LinkSetAlias(peer, spec.OwnerAlias); err != nil {
		return VethHandle{}, err
	}
	return VethHandle{HostIndex: host.Attrs().Index, GuestIndex: peer.Attrs().Index, OwnerAlias: spec.OwnerAlias, HostEnd: spec.HostEnd, GuestEnd: spec.GuestEnd, NetnsName: b.proof.Namespace}, nil
}

// MoveVethPeer never reopens the source namespace by its reusable name.
func (b *OwnedBridge) MoveVethPeer(ctx context.Context, pair VethHandle, destinationFD int) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	host, err := b.handle.LinkByIndex(pair.HostIndex)
	if err != nil {
		return err
	}
	peer, err := b.handle.LinkByIndex(pair.GuestIndex)
	if err != nil {
		return err
	}
	if pair.OwnerAlias == "" || host.Type() != "veth" || host.Attrs().Name != pair.HostEnd || host.Attrs().Alias != pair.OwnerAlias || peer.Type() != "veth" || peer.Attrs().Name != pair.GuestEnd || peer.Attrs().Alias != pair.OwnerAlias || peer.Attrs().ParentIndex != pair.HostIndex {
		return fmt.Errorf("veth incarnation changed before move")
	}
	return b.handle.LinkSetNsFd(peer, destinationFD)
}
