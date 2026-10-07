package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/vishvananda/netlink"
)

// BridgeTarget references a network's durable ownership anchor. Identity is
// independent of the node identity used to own an attachment.
type BridgeTarget struct {
	Managed          *substrate.ManagedBridgeTarget `json:"managed,omitempty"`
	Identity         substrate.OperationIdentity    `json:"identity"`
	NetworkNamespace string                         `json:"network_namespace"`
	Bridge           string                         `json:"bridge"`
	Root             bool                           `json:"root"`
}

type resolvedBridge struct {
	AttachmentLink
	NetworkNamespace                string
	NamespaceDevice, NamespaceInode uint64
}

func (d Driver) bridgeTargetProof(ctx context.Context, target BridgeTarget) (resolvedBridge, error) {
	invalid := func(message string, err error) (resolvedBridge, error) {
		return resolvedBridge{}, driver.Wrap(driver.ErrorInvalidState, "network", message, err)
	}
	if err := ctx.Err(); err != nil {
		return resolvedBridge{}, err
	}
	if err := target.Identity.Validate(); err != nil {
		return invalid("target network identity required; rebuild old state", err)
	}
	if target.Managed != nil {
		proof := *target.Managed
		if networkAlias(proof.Identity) != networkAlias(target.Identity) {
			return invalid("managed bridge target generation conflict", nil)
		}
		if proof.Version != 1 || proof.Bridge == "" || proof.Bridge != target.Bridge || proof.NetworkID == "" || proof.Index <= 0 || proof.Device == 0 || proof.Inode == 0 || proof.Alias != networkAlias(proof.Identity) {
			return invalid("managed bridge proof invalid; rebuild network", nil)
		}
		if err := verifyManagedBridge(ctx, proof); err != nil {
			return resolvedBridge{}, err
		}
		return resolvedBridge{AttachmentLink: AttachmentLink{Version: 1, Name: proof.Bridge, Device: proof.Device, Inode: proof.Inode, Index: proof.Index, Kind: "bridge", Alias: proof.Alias}}, nil
	}
	if target.NetworkNamespace == "" || target.NetworkNamespace == "." || target.NetworkNamespace == ".." || strings.ContainsAny(target.NetworkNamespace, "/\\") {
		return invalid("managed target network namespace required", nil)
	}
	raw, err := os.ReadFile(d.ownershipPath(target.NetworkNamespace))
	if os.IsNotExist(err) {
		return invalid("target network ownership anchor missing; rebuild network", err)
	}
	if err != nil {
		return resolvedBridge{}, networkQueryError("read target network anchor", err)
	}
	var marker networkOwnership
	if err := json.Unmarshal(raw, &marker); err != nil {
		return invalid("corrupt target network anchor; rebuild network", err)
	}
	if marker.Version != 2 {
		return invalid("unsupported target network anchor; rebuild network", nil)
	}
	if networkAlias(marker.Identity) != networkAlias(target.Identity) || marker.Spec.Name != target.NetworkNamespace {
		return invalid("target network generation identity conflict", nil)
	}
	if err := validateNetworkLinkProofs(marker); err != nil {
		return resolvedBridge{}, err
	}
	namespace, name := marker.Spec.Name, marker.Spec.Bridge
	if target.Root {
		namespace, name = "", marker.Spec.RootBridge
	}
	if name == "" || name != target.Bridge {
		return invalid("target bridge is not owned by network", nil)
	}
	for _, proof := range marker.Links {
		if proof.Namespace == namespace && proof.Name == name && proof.Kind == "bridge" {
			return resolvedBridge{AttachmentLink: proof, NetworkNamespace: marker.Spec.Name, NamespaceDevice: marker.NamespaceDevice, NamespaceInode: marker.NamespaceInode}, nil
		}
	}
	return invalid("target bridge incarnation proof missing; rebuild network", nil)
}

// OwnedBridge pins the verified namespace and serializes against cooperative
// network Create/Delete. It must stay open until all attachment effects finish.
type OwnedBridge struct {
	managed   *substrate.ManagedBridgeTarget
	networkNS *os.File
	ns        *os.File
	handle    *netlink.Handle
	proof     AttachmentLink
	bridge    *netlink.Bridge
	unlock    func()
}

func OpenOwnedBridge(ctx context.Context, target BridgeTarget) (*OwnedBridge, error) {
	return (Driver{}).openOwnedBridge(ctx, target)
}

func (d Driver) openOwnedBridge(ctx context.Context, target BridgeTarget) (*OwnedBridge, error) {
	unlock, err := d.lockNetworks(ctx)
	if err != nil {
		return nil, err
	}
	b, err := d.pinBridge(ctx, target)
	if err != nil {
		unlock()
		return nil, err
	}
	b.unlock = unlock
	return b, nil
}

func (d Driver) pinBridge(ctx context.Context, target BridgeTarget) (*OwnedBridge, error) {
	proof, err := d.bridgeTargetProof(ctx, target)
	if err != nil {
		return nil, err
	}
	ns, h, err := openAttachmentNamespace(proof.Namespace)
	if err != nil {
		return nil, networkQueryError("open target namespace", err)
	}
	b := &OwnedBridge{ns: ns, handle: h, proof: proof.AttachmentLink, managed: target.Managed}
	if target.Root && target.Managed == nil {
		networkNS, err := os.Open("/var/run/netns/" + proof.NetworkNamespace)
		if err != nil {
			b.Close()
			return nil, err
		}
		b.networkNS = networkNS
		if err := checkNamespaceFile(networkNS, proof.NamespaceDevice, proof.NamespaceInode); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Check(ctx); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *OwnedBridge) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.managed != nil {
		if err := verifyManagedBridge(ctx, *b.managed); err != nil {
			return err
		}
	}
	if err := checkNamespaceFile(b.ns, b.proof.Device, b.proof.Inode); err != nil {
		return err
	}
	link, err := b.handle.LinkByIndex(b.proof.Index)
	if err != nil {
		return networkQueryError("inspect target bridge incarnation", err)
	}
	bridge, ok := link.(*netlink.Bridge)
	if !ok || link.Attrs().Name != b.proof.Name || link.Attrs().Alias != b.proof.Alias {
		return driver.Wrap(driver.ErrorInvalidState, "network", "target bridge replaced", nil)
	}
	b.bridge = bridge
	return nil
}

func (b *OwnedBridge) Close() {
	if b.networkNS != nil {
		b.networkNS.Close()
		b.networkNS = nil
	}
	if b.handle != nil {
		b.handle.Close()
		b.handle = nil
	}
	if b.ns != nil {
		b.ns.Close()
		b.ns = nil
	}
	if b.unlock != nil {
		b.unlock()
		b.unlock = nil
	}
}

// LockBridgeTargets validates cold-plug targets under one host lock. The lock
// remains held through the caller's daemon start; external admins are not fenced.
func LockBridgeTargets(ctx context.Context, targets []BridgeTarget) ([]*OwnedBridge, func(), error) {
	d := Driver{}
	unlock, err := d.lockNetworks(ctx)
	if err != nil {
		return nil, nil, err
	}
	var bridges []*OwnedBridge
	release := func() {
		for _, b := range bridges {
			b.Close()
		}
		unlock()
	}
	for _, target := range targets {
		b, err := d.pinBridge(ctx, target)
		if err != nil {
			release()
			return nil, nil, err
		}
		bridges = append(bridges, b)
	}
	return bridges, release, nil
}

func (b *OwnedBridge) connect(ctx context.Context, link netlink.Link) error {
	if err := b.Check(ctx); err != nil {
		return err
	}
	if err := b.handle.LinkSetMaster(link, b.bridge); err != nil {
		return fmt.Errorf("connect owned bridge: %w", err)
	}
	return b.handle.LinkSetUp(link)
}
