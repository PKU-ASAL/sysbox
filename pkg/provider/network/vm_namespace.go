package network

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// VMNamespace is owned by exactly one node generation, independently of any
// network. Its link inventory is stored in that provider's durable manifest.
type VMNamespace struct {
	Version  int                         `json:"version"`
	Name     string                      `json:"name"`
	Identity substrate.OperationIdentity `json:"identity"`
	Device   uint64                      `json:"device"`
	Inode    uint64                      `json:"inode"`
}

func VMNamespaceName(id substrate.OperationIdentity) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", id.OwnerUID, id.TopologyUID, id.ResourceUID, id.Generation)))
	return fmt.Sprintf("sysbox-vm-%x", sum[:12])
}
func (p VMNamespace) Validate(id substrate.OperationIdentity) error {
	a, b := p.Identity, id
	a.ActionID = "namespace"
	b.ActionID = "namespace"
	if id.Validate() != nil || p.Version != 1 || p.Name != VMNamespaceName(id) || a != b || p.Device == 0 || p.Inode == 0 {
		return fmt.Errorf("VM namespace identity proof invalid")
	}
	return nil
}
func vmCircuitName(prefix, alias string) string {
	sum := sha256.Sum256([]byte(alias))
	return fmt.Sprintf("%s-%x", prefix, sum[:6])
}

// NewVMNamespace never adopts an existing anchor. The caller persists its
// pending intent before this call and checkpoints the returned inode before
// any attachment. A lost creation reply remains unknown, never name ownership.
func NewVMNamespace(ctx context.Context, id substrate.OperationIdentity) (VMNamespace, error) {
	p := VMNamespace{Version: 1, Name: VMNamespaceName(id), Identity: id}
	if err := id.Validate(); err != nil {
		return p, err
	}
	unlock, err := (Driver{}).lockNetworks(ctx)
	if err != nil {
		return p, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return p, err
	}
	ns, err := createExclusiveNetns(p.Name)
	if err != nil {
		return p, err
	}
	defer ns.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(ns), &st); err != nil {
		return p, err
	}
	p.Device, p.Inode = uint64(st.Dev), st.Ino
	return p, nil
}
func openVMNamespace(ctx context.Context, p VMNamespace) (*os.File, *netlink.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := p.Validate(p.Identity); err != nil {
		return nil, nil, err
	}
	ns, h, err := openAttachmentNamespace(p.Name)
	if err != nil {
		return nil, nil, err
	}
	if err = checkNamespaceFile(ns, p.Device, p.Inode); err != nil {
		ns.Close()
		h.Close()
		return nil, nil, err
	}
	return ns, h, nil
}

// Configure a namespace before links are made UP. No guest-facing interface
// receives a host address, and L3 forwarding is never enabled in the VM fabric.
func ConfigureVMNamespace(ctx context.Context, p VMNamespace) error {
	ns, h, err := openVMNamespace(ctx, p)
	if err != nil {
		return err
	}
	defer ns.Close()
	defer h.Close()
	return inVMNamespace(ns, func() error {
		for _, setting := range []struct{ path, value string }{
			{"/proc/sys/net/ipv4/ip_forward", "0"},
			{"/proc/sys/net/ipv6/conf/all/forwarding", "0"},
			{"/proc/sys/net/ipv6/conf/default/disable_ipv6", "1"},
			{"/proc/sys/net/ipv6/conf/all/disable_ipv6", "1"},
		} {
			if err := os.WriteFile(setting.path, []byte(setting.value), 0600); err != nil {
				if os.IsNotExist(err) && setting.path != "/proc/sys/net/ipv4/ip_forward" {
					continue
				}
				return err
			}
		}
		return ctx.Err()
	})
}
func inVMNamespace(ns *os.File, action func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		original, err := netns.Get()
		if err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}
		defer original.Close()
		if err = netns.Set(netns.NsHandle(ns.Fd())); err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}
		err = action()
		if restore := netns.Set(original); restore != nil {
			done <- fmt.Errorf("restore VM namespace: %w", restore)
			return
		}
		runtime.UnlockOSThread()
		done <- err
	}()
	return <-done
}
func checkVMNoRouting(ns *os.File) error {
	return inVMNamespace(ns, func() error {
		for _, path := range []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"} {
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) && path != "/proc/sys/net/ipv4/ip_forward" {
				continue
			}
			if err != nil {
				return err
			}
			if len(raw) == 0 || raw[0] != '0' {
				return fmt.Errorf("VM fabric must not forward IP packets")
			}
		}
		return nil
	})
}

// DeleteVMNamespace only removes a proved, empty namespace after all NICs have
// been reclaimed and the VMM is stopped. Unaccounted devices block deletion.
func DeleteVMNamespace(ctx context.Context, p VMNamespace) error {
	unlock, err := (Driver{}).lockNetworks(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ns, h, err := openVMNamespace(ctx, p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer ns.Close()
	defer h.Close()
	links, err := h.LinkList()
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Type() != "device" || link.Attrs().Name != "lo" {
			return fmt.Errorf("VM namespace still has network devices")
		}
	}
	return anchorCommand("--anchor-delete", p.Name, fmt.Sprint(p.Device), fmt.Sprint(p.Inode))
}

func vmLinkProof(p VMNamespace, name, kind, alias string, index int) AttachmentLink {
	return AttachmentLink{Version: 1, Namespace: p.Name, Device: p.Device, Inode: p.Inode, Name: name, Kind: kind, Alias: alias, Index: index}
}
func validateVMLink(h *netlink.Handle, p AttachmentLink, missingOK bool) (netlink.Link, error) {
	link, err := h.LinkByName(p.Name)
	if missingOK && missingLink(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Version != 1 || p.Index <= 0 || link.Attrs().Index != p.Index || link.Type() != p.Kind || link.Attrs().Alias != p.Alias {
		return nil, driver.Wrap(driver.ErrorInvalidState, "network", fmt.Sprintf("VM link identity changed: %s expected index=%d kind=%s alias=%q; actual index=%d kind=%s alias=%q", p.Name, p.Index, p.Kind, p.Alias, link.Attrs().Index, link.Type(), link.Attrs().Alias), nil)
	}
	return link, nil
}
func vmLinkHasNoAddress(h *netlink.Handle, link netlink.Link) error {
	addresses, err := h.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	if len(addresses) != 0 {
		return fmt.Errorf("VM fabric interface has unexpected host addresses")
	}
	return nil
}

func createVMRecordedLink(ctx context.Context, ns *os.File, h *netlink.Handle, p AttachmentLink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := h.LinkByName(p.Name); err == nil {
		return fmt.Errorf("refusing existing VM link")
	} else if !missingLink(err) {
		return err
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = p.Name
	attrs.Index = p.Index
	attrs.Alias = p.Alias
	if p.Kind == "bridge" {
		if err := h.LinkAdd(&netlink.Bridge{LinkAttrs: attrs}); err != nil {
			return err
		}
		link, err := h.LinkByIndex(p.Index)
		if err != nil {
			return err
		}
		if link.Type() != p.Kind || link.Attrs().Name != p.Name {
			return fmt.Errorf("new VM bridge identity changed")
		}
		return h.LinkSetAlias(link, p.Alias)
	}
	if p.Kind != "tuntap" {
		return fmt.Errorf("invalid VM device type")
	}
	return inVMNamespace(ns, func() error {
		tun, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer tun.Close()
		if err = unix.IoctlSetPointerInt(int(tun.Fd()), unix.TUNSETIFINDEX, p.Index); err != nil {
			return err
		}
		req, err := unix.NewIfreq(p.Name)
		if err != nil {
			return err
		}
		req.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
		if err = unix.IoctlIfreq(int(tun.Fd()), unix.TUNSETIFF, req); err != nil {
			return err
		}
		link, err := h.LinkByIndex(p.Index)
		if err != nil {
			return err
		}
		if link.Type() != "tuntap" || link.Attrs().Name != p.Name {
			return fmt.Errorf("new VM TAP identity changed")
		}
		if err = h.LinkSetAlias(link, p.Alias); err != nil {
			return err
		}
		return unix.IoctlSetInt(int(tun.Fd()), unix.TUNSETPERSIST, 1)
	})
}

func VMNamespaceAbsent(ctx context.Context, p VMNamespace) error {
	if err := p.Validate(p.Identity); err != nil {
		return err
	}
	f, h, err := openVMNamespace(ctx, p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	f.Close()
	h.Close()
	return fmt.Errorf("VM namespace residue remains")
}

// VMNamespaceNameAbsent is only a negative check. A matching name never grants
// adoption or deletion authority when the provider journal is unavailable.
func VMNamespaceNameAbsent(id substrate.OperationIdentity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	_, err := os.Lstat("/var/run/netns/" + VMNamespaceName(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("VM namespace exists without complete provider ownership evidence")
}
