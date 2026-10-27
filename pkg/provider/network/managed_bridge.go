package network

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"syscall"
)

func LockManagedNetworks(ctx context.Context) (func(), error) { return (Driver{}).lockNetworks(ctx) }

func PreflightManagedBridge(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	caps := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &caps[0]); err != nil {
		return err
	}
	if caps[0].Effective&(1<<unix.CAP_NET_ADMIN) == 0 {
		return fmt.Errorf("mixed-substrate managed bridge requires CAP_NET_ADMIN on the owning Agent")
	}
	if caps[0].Effective&(1<<unix.CAP_SYS_ADMIN) == 0 {
		return fmt.Errorf("mixed-substrate managed bridge requires CAP_SYS_ADMIN to pin network namespace handles")
	}
	return nil
}

func ManagedBridgeName(id substrate.OperationIdentity) string {
	sum := sha256.Sum256([]byte(networkAlias(id)))
	return fmt.Sprintf("sb-%x", sum[:6])
}
func managedBridgePath(provider, id string) string {
	root, _ := (Driver{}).roots()
	sum := sha256.Sum256([]byte(provider + "\x00" + id))
	return filepath.Join(root, fmt.Sprintf("managed-%x.json", sum))
}

// RecordManagedBridge is called once, immediately after a successful provider
// create, under LockManagedNetworks. It must never be used to adopt an existing
// network whose creation response or durable proof was lost.
func RecordManagedBridge(ctx context.Context, provider, id, name string, identity substrate.OperationIdentity) (*substrate.ManagedBridgeTarget, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	ns, h, err := openAttachmentNamespace("")
	if err != nil {
		return nil, err
	}
	defer ns.Close()
	defer h.Close()
	link, err := h.LinkByName(name)
	if err != nil {
		return nil, err
	}
	if link.Type() != "bridge" || link.Attrs().Alias != "" {
		return nil, fmt.Errorf("new managed bridge has unexpected type or ownership")
	}
	info, err := ns.Stat()
	if err != nil {
		return nil, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	proof := substrate.ManagedBridgeTarget{Version: 1, Provider: provider, NetworkID: id, Identity: identity, Bridge: name, Device: uint64(stat.Dev), Inode: stat.Ino, Index: link.Attrs().Index, Alias: networkAlias(identity)}
	if _, err := os.Stat(managedBridgePath(provider, id)); !os.IsNotExist(err) {
		return nil, fmt.Errorf("managed bridge anchor already exists or cannot be inspected")
	}
	if err := writeNetworkRecord(managedBridgePath(provider, id), proof); err != nil {
		return nil, err
	}
	current, err := h.LinkByIndex(proof.Index)
	if err != nil {
		return nil, err
	}
	if current.Type() != "bridge" || current.Attrs().Name != name || current.Attrs().Alias != "" {
		return nil, fmt.Errorf("managed bridge changed before ownership tag")
	}
	if err := h.LinkSetAlias(current, proof.Alias); err != nil {
		return nil, err
	}
	return &proof, nil
}

func LoadManagedBridge(provider, id string) (*substrate.ManagedBridgeTarget, error) {
	raw, err := os.ReadFile(managedBridgePath(provider, id))
	if err != nil {
		return nil, driver.Wrap(driver.ErrorInvalidState, provider, "managed bridge anchor unavailable; rebuild network", err)
	}
	var proof substrate.ManagedBridgeTarget
	if err := json.Unmarshal(raw, &proof); err != nil {
		return nil, err
	}
	if proof.Version != 1 || proof.Provider != provider || proof.NetworkID != id {
		return nil, fmt.Errorf("managed bridge anchor invalid; rebuild network")
	}
	return &proof, nil
}

func InspectManagedBridge(ctx context.Context, proof substrate.ManagedBridgeTarget) error {
	ns, h, err := openAttachmentNamespace("")
	if err != nil {
		return err
	}
	b := &OwnedBridge{ns: ns, handle: h, proof: AttachmentLink{Version: 1, Device: proof.Device, Inode: proof.Inode, Name: proof.Bridge, Index: proof.Index, Kind: "bridge", Alias: proof.Alias}}
	defer b.Close()
	return b.Check(ctx)
}

func RemoveManagedBridge(provider, id string) error {
	path := managedBridgePath(provider, id)
	if err := os.Remove(path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func verifyManagedBridge(ctx context.Context, proof substrate.ManagedBridgeTarget) error {
	provider, err := driver.DefaultRegistry.RequireNetwork(proof.Provider)
	if err != nil {
		return err
	}
	verifier, ok := provider.(driver.ManagedBridgeVerifier)
	if !ok {
		return driver.Wrap(driver.ErrorUnsupported, proof.Provider, "managed bridge ownership verification unsupported", nil)
	}
	return verifier.VerifyManagedBridge(ctx, proof)
}
