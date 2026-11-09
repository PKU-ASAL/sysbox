package network

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"github.com/vishvananda/netlink"
)

// VMCircuit has a private bridge per logical NIC. No shared bridge joins two
// networks in the VMM namespace. The remote end belongs to the node, not the
// network bridge; deleting it must never delete that bridge.
type VMCircuit struct {
	Version int            `json:"version"`
	Alias   string         `json:"alias"`
	Bridge  AttachmentLink `json:"bridge"`
	Local   AttachmentLink `json:"local"`
	Remote  AttachmentLink `json:"remote"`
	Tap     AttachmentLink `json:"tap"`
}

func (p VMCircuit) Validate(ns VMNamespace) error {
	if ns.Validate(ns.Identity) != nil || p.Version != 1 || p.Alias == "" {
		return fmt.Errorf("invalid VM circuit proof")
	}
	seen := map[string]bool{}
	for _, entry := range []struct {
		p            AttachmentLink
		kind, prefix string
	}{{p.Bridge, "bridge", "b"}, {p.Local, "veth", "v"}, {p.Tap, "tuntap", "t"}} {
		x := entry.p
		if x.Version != 1 || x.Namespace != ns.Name || x.Device != ns.Device || x.Inode != ns.Inode || x.Index <= 0 || x.Alias != p.Alias || x.Kind != entry.kind || x.Name != vmCircuitName(entry.prefix, p.Alias) || seen[x.Name] {
			return fmt.Errorf("invalid local VM circuit identity")
		}
		seen[x.Name] = true
	}
	if p.Remote.Version != 1 || p.Remote.Namespace == "" || p.Remote.Namespace == ns.Name || p.Remote.Inode == 0 || p.Remote.Device == 0 || p.Remote.Index <= 0 || p.Remote.Kind != "veth" || p.Remote.Alias != p.Alias || p.Remote.Name != vmCircuitName("p", p.Alias) {
		return fmt.Errorf("invalid remote VM circuit identity")
	}
	return nil
}

// PrepareVMCircuit allocates indexes under the network lock, without creating
// links. Caller must durably checkpoint this intent before CreateVMCircuit.
func (b *OwnedBridge) PrepareVMCircuit(ctx context.Context, ns VMNamespace, alias string) (VMCircuit, error) {
	p := VMCircuit{Version: 1, Alias: alias}
	if err := b.Check(ctx); err != nil {
		return p, err
	}
	if b.proof.Namespace == "" || b.proof.Namespace == ns.Name {
		return p, fmt.Errorf("VM circuits require distinct isolated target networks")
	}
	f, h, err := openVMNamespace(ctx, ns)
	if err != nil {
		return p, err
	}
	defer f.Close()
	defer h.Close()
	indices := make([]int, 4)
	for i := range indices {
		indices[i], err = newAttachmentIndex()
		if err != nil {
			return p, err
		}
	}
	p.Bridge = vmLinkProof(ns, vmCircuitName("b", alias), "bridge", alias, indices[0])
	p.Local = vmLinkProof(ns, vmCircuitName("v", alias), "veth", alias, indices[1])
	p.Tap = vmLinkProof(ns, vmCircuitName("t", alias), "tuntap", alias, indices[2])
	p.Remote = AttachmentLink{Version: 1, Namespace: b.proof.Namespace, Device: b.proof.Device, Inode: b.proof.Inode, Name: vmCircuitName("p", alias), Index: indices[3], Kind: "veth", Alias: alias}
	return p, p.Validate(ns)
}

// CreateVMCircuit is deliberately not an adoption API. Any pre-existing name
// rejects the effect; interrupted intents require explicit recovery evidence.
func (b *OwnedBridge) CreateVMCircuit(ctx context.Context, ns VMNamespace, p *VMCircuit, checkpoint func(VMCircuit) error) error {
	if p == nil || checkpoint == nil {
		return fmt.Errorf("circuit intent checkpoint required")
	}
	if err := p.Validate(ns); err != nil {
		return err
	}
	if err := b.Check(ctx); err != nil {
		return err
	}
	if p.Remote.Namespace != b.proof.Namespace || p.Remote.Device != b.proof.Device || p.Remote.Inode != b.proof.Inode {
		return fmt.Errorf("remote circuit namespace changed")
	}
	f, h, err := openVMNamespace(ctx, ns)
	if err != nil {
		return err
	}
	defer f.Close()
	defer h.Close()
	if err = checkVMNoRouting(f); err != nil {
		return err
	}
	for _, entry := range []struct {
		h *netlink.Handle
		p AttachmentLink
	}{{h, p.Bridge}, {h, p.Local}, {h, p.Tap}, {b.handle, p.Remote}} {
		if _, err := entry.h.LinkByName(entry.p.Name); err == nil {
			return fmt.Errorf("VM circuit name already exists")
		} else if !missingLink(err) {
			return err
		}
	}
	if err = createVMRecordedLink(ctx, f, h, p.Bridge); err != nil {
		return err
	}
	bridge, err := validateVMLink(h, p.Bridge, false)
	if err != nil {
		return err
	}
	// Peer ifindex is allocated by the kernel. Record its actual index before
	// namespace movement. If that checkpoint is lost, the intent remains frozen.
	attrs := netlink.NewLinkAttrs()
	attrs.Name = p.Local.Name
	attrs.Index = p.Local.Index
	attrs.Alias = p.Alias
	veth := &netlink.Veth{LinkAttrs: attrs, PeerName: p.Remote.Name}
	if err = h.LinkAdd(veth); err != nil {
		return err
	}
	created, err := h.LinkByIndex(p.Local.Index)
	if err != nil {
		return err
	}
	if created.Type() != "veth" || created.Attrs().Name != p.Local.Name {
		return fmt.Errorf("new VM veth identity changed")
	}
	if err = h.LinkSetAlias(created, p.Alias); err != nil {
		return err
	}
	local, err := validateVMLink(h, p.Local, false)
	if err != nil {
		return err
	}
	peer, err := h.LinkByName(p.Remote.Name)
	if err != nil {
		return err
	}
	if peer.Type() != "veth" || peer.Attrs().ParentIndex != local.Attrs().Index {
		return fmt.Errorf("VM circuit peer conflict")
	}
	if err = h.LinkSetAlias(peer, p.Alias); err != nil {
		return err
	}
	p.Remote.Index = peer.Attrs().Index
	if err = checkpoint(*p); err != nil {
		return err
	}
	if err = h.LinkSetNsFd(peer, int(b.ns.Fd())); err != nil {
		return err
	}
	peer, err = validateVMLink(b.handle, p.Remote, false)
	if err != nil {
		return err
	}
	if err = h.LinkSetMaster(local, bridge); err != nil {
		return err
	}
	if err = b.connect(ctx, peer); err != nil {
		return err
	}
	if err = createVMRecordedLink(ctx, f, h, p.Tap); err != nil {
		return err
	}
	tap, err := validateVMLink(h, p.Tap, false)
	if err != nil {
		return err
	}
	if err = h.LinkSetMaster(tap, bridge); err != nil {
		return err
	}
	for _, link := range []netlink.Link{bridge, local, tap} {
		if err = h.LinkSetUp(link); err != nil {
			return err
		}
	}
	return b.ObserveVMCircuit(ctx, ns, *p)
}
func (b *OwnedBridge) ObserveVMCircuit(ctx context.Context, ns VMNamespace, p VMCircuit) error {
	if err := p.Validate(ns); err != nil {
		return err
	}
	if err := b.ObserveAttachment(ctx, p.Remote, p.Alias); err != nil {
		return err
	}
	f, h, err := openVMNamespace(ctx, ns)
	if err != nil {
		return err
	}
	defer f.Close()
	defer h.Close()
	if err = checkVMNoRouting(f); err != nil {
		return err
	}
	bridge, err := validateVMLink(h, p.Bridge, false)
	if err != nil {
		return err
	}
	local, err := validateVMLink(h, p.Local, false)
	if err != nil {
		return err
	}
	tap, err := validateVMLink(h, p.Tap, false)
	if err != nil {
		return err
	}
	if local.Attrs().MasterIndex != bridge.Attrs().Index || tap.Attrs().MasterIndex != bridge.Attrs().Index {
		return fmt.Errorf("VM circuit port moved to another bridge")
	}
	if err = b.ObserveVethPeer(ctx, p.Remote, h, int(f.Fd()), local); err != nil {
		return err
	}
	for _, link := range []netlink.Link{bridge, local, tap} {
		if err = vmLinkHasNoAddress(h, link); err != nil {
			return err
		}
	}
	return nil
}
func (b *OwnedBridge) SetVMCircuitTapOwner(ctx context.Context, ns VMNamespace, p VMCircuit, uid uint32) error {
	if err := b.ObserveVMCircuit(ctx, ns, p); err != nil {
		return err
	}
	f, h, err := openVMNamespace(ctx, ns)
	if err != nil {
		return err
	}
	defer f.Close()
	defer h.Close()
	local := &OwnedBridge{ns: f, handle: h, proof: p.Bridge}
	return local.SetTapOwner(ctx, p.Tap, p.Alias, uid)
}

// VerifyVMCircuitInventory detects unexpected local bridges/ports before VMM
// launch. It must be called while network targets remain locked.
func VerifyVMCircuitInventory(ctx context.Context, ns VMNamespace, circuits []VMCircuit) error {
	f, h, err := openVMNamespace(ctx, ns)
	if err != nil {
		return err
	}
	defer f.Close()
	defer h.Close()
	if err = checkVMNoRouting(f); err != nil {
		return err
	}
	expected := map[string]AttachmentLink{}
	for _, c := range circuits {
		if err = c.Validate(ns); err != nil {
			return err
		}
		for _, p := range []AttachmentLink{c.Bridge, c.Local, c.Tap} {
			if _, ok := expected[p.Name]; ok {
				return fmt.Errorf("shared VM circuit port")
			}
			expected[p.Name] = p
		}
	}
	links, err := h.LinkList()
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Attrs().Name == "lo" && link.Type() == "device" {
			continue
		}
		p, ok := expected[link.Attrs().Name]
		if !ok {
			return fmt.Errorf("unrecorded VM network device")
		}
		if _, err = validateVMLink(h, p, false); err != nil {
			return err
		}
		delete(expected, p.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("VM network inventory incomplete")
	}
	return nil
}

// DeleteVMCircuit prevalidates every extant component before the first delete.
// A detached peer, changed namespace or unexpected bridge member is unknown.
func DeleteVMCircuit(ctx context.Context, ns VMNamespace, p VMCircuit) error {
	if err := p.Validate(ns); err != nil {
		return err
	}
	unlock, err := (Driver{}).lockNetworks(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	var local *netlink.Handle
	var f *os.File
	f, local, err = openVMNamespace(ctx, ns)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		defer f.Close()
		defer local.Close()
	}
	remoteNS, remote, remoteErr := openAttachmentNamespace(p.Remote.Namespace)
	if remoteErr != nil && !os.IsNotExist(remoteErr) {
		return remoteErr
	}
	if remoteErr == nil {
		defer remoteNS.Close()
		defer remote.Close()
		if err = checkNamespaceFile(remoteNS, p.Remote.Device, p.Remote.Inode); err != nil {
			return err
		}
	}
	proofs := []AttachmentLink{p.Tap, p.Local, p.Bridge}
	if local != nil {
		links, err := local.LinkList()
		if err != nil {
			return err
		}
		for _, link := range links {
			if link.Attrs().MasterIndex == p.Bridge.Index && link.Attrs().Name != p.Tap.Name && link.Attrs().Name != p.Local.Name {
				return fmt.Errorf("unowned port on VM circuit bridge")
			}
		}
		for _, proof := range proofs {
			if _, err = validateVMLink(local, proof, true); err != nil {
				return err
			}
		}
	}
	if remote != nil {
		if _, err = validateVMLink(remote, p.Remote, true); err != nil {
			return err
		}
	}
	// Veth deletion affects its peer too. Never delete either endpoint unless
	// the actual kernel peer relation matches the two persisted namespace proofs.
	if local != nil {
		link, e := validateVMLink(local, p.Local, true)
		if e != nil {
			return e
		}
		if link != nil {
			if remote == nil {
				return fmt.Errorf("VM veth peer namespace unavailable")
			}
			peer, e := validateVMLink(remote, p.Remote, false)
			if e != nil {
				return e
			}
			remoteID, e := local.GetNetNsIdByFd(int(remoteNS.Fd()))
			if e != nil {
				return e
			}
			localID, e := remote.GetNetNsIdByFd(int(f.Fd()))
			if e != nil {
				return e
			}
			if !sameVethPeerNamespaces(link, peer, remoteID, localID) {
				return fmt.Errorf("VM circuit peer identity changed")
			}
		} else if remote != nil {
			peer, e := validateVMLink(remote, p.Remote, true)
			if e != nil {
				return e
			}
			if peer != nil {
				return fmt.Errorf("VM circuit remote peer remains without local proof")
			}
		}
	} else if remote != nil {
		peer, e := validateVMLink(remote, p.Remote, true)
		if e != nil {
			return e
		}
		if peer != nil {
			return fmt.Errorf("VM circuit remote peer remains without VM namespace")
		}
	}
	if local != nil {
		for _, proof := range proofs {
			link, err := validateVMLink(local, proof, true)
			if err != nil {
				return err
			}
			if link != nil {
				if err = local.LinkDel(link); err != nil {
					return err
				}
			}
		}
	}
	if remote != nil {
		link, err := validateVMLink(remote, p.Remote, true)
		if err != nil {
			return err
		}
		if link != nil {
			if err = remote.LinkDel(link); err != nil {
				return err
			}
		}
	}
	return nil
}
func VMCircuitAbsent(ctx context.Context, ns VMNamespace, p VMCircuit) error {
	if err := p.Validate(ns); err != nil {
		return err
	}
	for _, proof := range []AttachmentLink{p.Bridge, p.Local, p.Tap, p.Remote} {
		ns, h, err := openAttachmentNamespace(proof.Namespace)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = checkNamespaceFile(ns, proof.Device, proof.Inode)
		if err == nil {
			var link netlink.Link
			link, err = validateVMLink(h, proof, true)
			if err == nil && link != nil {
				err = fmt.Errorf("VM circuit residue remains")
			}
		}
		ns.Close()
		h.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func SameVMCircuit(a, b *VMCircuit) bool { return reflect.DeepEqual(a, b) }
