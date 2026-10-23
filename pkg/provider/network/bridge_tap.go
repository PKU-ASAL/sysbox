package network

import (
	"context"
	"fmt"
	"github.com/vishvananda/netlink"
)

// AttachTap creates or reuses only an owned TAP, then moves it to the pinned
// namespace FD and connects by bridge ifindex. No shell/name-based effects.
func (b *OwnedBridge) AttachTap(ctx context.Context, name, alias string) error {
	if alias == "" {
		return fmt.Errorf("TAP ownership alias required")
	}
	if err := b.Check(ctx); err != nil {
		return err
	}
	link, err := b.handle.LinkByName(name)
	if err != nil && !missingLink(err) {
		return err
	}
	if missingLink(err) {
		ns, root, err := openAttachmentNamespace("")
		if err != nil {
			return err
		}
		defer ns.Close()
		defer root.Close()
		link, err = root.LinkByName(name)
		if err != nil && !missingLink(err) {
			return err
		}
		if missingLink(err) {
			if err := CreateOwnedTap(ctx, name, alias); err != nil {
				return err
			}
			link, err = root.LinkByName(name)
			if err != nil {
				return err
			}
		}
		if err := validateTap(link, name, alias); err != nil {
			return err
		}
		index := link.Attrs().Index
		if b.proof.Namespace != "" {
			if err := root.LinkSetNsFd(link, int(b.ns.Fd())); err != nil {
				return err
			}
		}
		link, err = b.handle.LinkByIndex(index)
		if err != nil {
			return err
		}
	}
	if err := validateTap(link, name, alias); err != nil {
		return err
	}
	return b.connect(ctx, link)
}

func validateTap(link netlink.Link, name, alias string) error {
	if link.Type() != "tuntap" || link.Attrs().Name != name || link.Attrs().Alias != alias {
		return fmt.Errorf("TAP ownership identity conflict")
	}
	return nil
}

func (b *OwnedBridge) ObserveAttachment(ctx context.Context, proof AttachmentLink, alias string) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	if proof.Namespace != b.proof.Namespace || proof.Device != b.proof.Device || proof.Inode != b.proof.Inode {
		return fmt.Errorf("attachment and bridge namespace identity conflict")
	}
	if err := validateAttachmentProof(proof, alias); err != nil {
		return err
	}
	link, err := b.handle.LinkByIndex(proof.Index)
	if err != nil {
		return fmt.Errorf("attachment link replaced or absent: %w", err)
	}
	if link.Type() != proof.Kind || link.Attrs().Name != proof.Name || link.Attrs().Alias != alias {
		return fmt.Errorf("attachment link replaced")
	}
	if link.Attrs().MasterIndex != b.proof.Index {
		return fmt.Errorf("attachment target bridge drifted")
	}
	return nil
}

func (b *OwnedBridge) CaptureAttachment(ctx context.Context, name, alias string) (AttachmentLink, error) {
	if err := b.Check(ctx); err != nil {
		return AttachmentLink{}, err
	}
	link, err := b.handle.LinkByName(name)
	if err != nil {
		return AttachmentLink{}, err
	}
	if alias == "" || link.Attrs().Alias != alias || (link.Type() != "veth" && link.Type() != "tuntap") || link.Attrs().MasterIndex != b.proof.Index {
		return AttachmentLink{}, fmt.Errorf("attachment ownership or target mismatch")
	}
	return networkLinkProof(b.ns, b.proof.Namespace, link, alias)
}

// RequireNoAttachmentPorts is a conservative absence proof for legacy NIC state
// without an exact TAP identity. Any bridge port or namespace veth/TAP, including detached or unrelated links,
// blocks it.
// It never detaches ports or assumes bridge ownership implies port ownership.
func (b *OwnedBridge) RequireNoAttachmentPorts(ctx context.Context) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	links, err := b.handle.LinkList()
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Attrs().MasterIndex == b.bridge.Attrs().Index || link.Type() == "veth" || link.Type() == "tuntap" {
			return fmt.Errorf("attachment port remains on proven bridge")
		}
	}
	return b.Check(ctx)
}
