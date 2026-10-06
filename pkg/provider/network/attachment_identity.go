package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type AttachmentLink struct {
	Version   int    `json:"version"`
	Namespace string `json:"namespace"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Name      string `json:"name"`
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	Alias     string `json:"alias"`
}

func AttachmentAlias(id substrate.OperationIdentity, name string) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("attachment logical name required")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", id.OwnerUID, id.TopologyUID, id.ResourceUID, id.Generation, name)))
	return fmt.Sprintf("sysbox-attachment:%x", sum), nil
}

func openAttachmentNamespace(namespace string) (*os.File, *netlink.Handle, error) {
	path := "/proc/self/ns/net"
	if namespace != "" {
		if namespace == "." || namespace == ".." || strings.ContainsAny(namespace, "/\\") {
			return nil, nil, fmt.Errorf("invalid attachment namespace")
		}
		path = filepath.Join("/var/run/netns", namespace)
	}
	ns, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	h, err := netlink.NewHandleAt(netns.NsHandle(ns.Fd()))
	if err != nil {
		ns.Close()
		return nil, nil, err
	}
	if err := h.SetSocketTimeout(time.Second); err != nil {
		h.Close()
		ns.Close()
		return nil, nil, err
	}
	return ns, h, nil
}

func CaptureAttachmentLink(ctx context.Context, namespace, name, alias string) (AttachmentLink, error) {
	if err := ctx.Err(); err != nil {
		return AttachmentLink{}, err
	}
	ns, h, err := openAttachmentNamespace(namespace)
	if err != nil {
		return AttachmentLink{}, err
	}
	defer ns.Close()
	defer h.Close()
	link, err := h.LinkByName(name)
	if err != nil {
		return AttachmentLink{}, err
	}
	if alias == "" || link.Attrs().Alias != alias {
		return AttachmentLink{}, driver.Wrap(driver.ErrorInvalidState, "network", fmt.Sprintf("attachment ownership conflict: expected %q, observed %q", alias, link.Attrs().Alias), nil)
	}
	info, err := ns.Stat()
	if err != nil {
		return AttachmentLink{}, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	return AttachmentLink{Version: 1, Namespace: namespace, Device: uint64(stat.Dev), Inode: stat.Ino, Name: name, Index: link.Attrs().Index, Kind: link.Type(), Alias: alias}, nil
}

// Verify and delete using one pinned namespace and the inspected ifindex.
func DeleteOwnedAttachment(ctx context.Context, proof AttachmentLink, alias string) error {
	err := useOwnedAttachment(ctx, proof, alias, true)
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return nil
	}
	return err
}

func ObserveOwnedAttachment(ctx context.Context, proof AttachmentLink, alias string) error {
	return useOwnedAttachment(ctx, proof, alias, false)
}

func useOwnedAttachment(ctx context.Context, proof AttachmentLink, alias string, remove bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if proof.Version != 1 || alias == "" || proof.Alias != alias || proof.Index <= 0 || proof.Inode == 0 || proof.Name == "" || (proof.Kind != "veth" && proof.Kind != "tuntap") {
		return driver.Wrap(driver.ErrorInvalidState, "network", "attachment proof missing or conflicting", nil)
	}
	ns, h, err := openAttachmentNamespace(proof.Namespace)
	if os.IsNotExist(err) {
		return driver.Wrap(driver.ErrorNotFound, "network", "attachment namespace absent", err)
	}
	if err != nil {
		return err
	}
	defer ns.Close()
	defer h.Close()
	info, err := ns.Stat()
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if uint64(stat.Dev) != proof.Device || stat.Ino != proof.Inode {
		return driver.Wrap(driver.ErrorInvalidState, "network", "attachment namespace replaced", nil)
	}
	link, err := h.LinkByName(proof.Name)
	if missingLink(err) {
		return driver.Wrap(driver.ErrorNotFound, "network", "attachment link absent", err)
	}
	if err != nil {
		return err
	}
	if link.Attrs().Index != proof.Index || link.Type() != proof.Kind || link.Attrs().Alias != alias {
		return driver.Wrap(driver.ErrorInvalidState, "network", "attachment link replaced", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if remove {
		return h.LinkDel(link)
	}
	return nil
}

// CreateOwnedTap requests a specific ifindex and an exclusive, nonpersistent
// TAP. Ownership is set by that index before making the device persistent.
func CreateOwnedTap(ctx context.Context, name, alias string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if alias == "" {
		return fmt.Errorf("attachment alias required")
	}
	ns, h, err := openAttachmentNamespace("")
	if err != nil {
		return err
	}
	defer ns.Close()
	defer h.Close()
	index, err := newAttachmentIndex()
	if err != nil {
		return err
	}
	tun, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tun.Close()
	if err := unix.IoctlSetPointerInt(int(tun.Fd()), unix.TUNSETIFINDEX, index); err != nil {
		return err
	}
	request, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	request.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
	if err := unix.IoctlIfreq(int(tun.Fd()), unix.TUNSETIFF, request); err != nil {
		return err
	}
	link, err := h.LinkByIndex(index)
	if err != nil {
		return err
	}
	if link.Attrs().Name != name || link.Type() != "tuntap" {
		return fmt.Errorf("new TAP identity mismatch")
	}
	if err := h.LinkSetAlias(link, alias); err != nil {
		return err
	}
	if err := h.LinkSetUp(link); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.IoctlSetInt(int(tun.Fd()), unix.TUNSETPERSIST, 1)
}

func newAttachmentIndex() (int, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(raw[:])&0x3fffffff) + 1024, nil
}
