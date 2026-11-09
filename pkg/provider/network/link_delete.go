package network

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/vishvananda/netlink"
)

type inspectedLinkDeleter interface {
	LinkDel(netlink.Link) error
	LinkByIndex(int) (netlink.Link, error)
}

// The caller has already checked namespace incarnation, ifindex, kind and alias.
// A veth peer can disappear in the kernel between that check and RTM_DELLINK.
// Accept only a second authoritative absence read in this same pinned namespace;
// never retry deletion by name/index or normalize unrelated kernel errors.
func deleteInspectedLink(h inspectedLinkDeleter, link netlink.Link) error {
	err := h.LinkDel(link)
	if err == nil || !missingLink(err) {
		return err
	}
	_, observed := h.LinkByIndex(link.Attrs().Index)
	if missingLink(observed) {
		return nil
	}
	if observed != nil {
		return fmt.Errorf("verify link absence after delete: %w", observed)
	}
	return driver.Wrap(driver.ErrorInvalidState, "network", "link index remains present after missing-device delete", err)
}
