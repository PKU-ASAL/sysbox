package network

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type networkOwnership struct {
	Links                           []AttachmentLink            `json:"links"`
	Version                         int                         `json:"version"`
	Identity                        substrate.OperationIdentity `json:"identity"`
	Spec                            driver.IsolatedNetworkSpec  `json:"spec"`
	NamespaceDevice, NamespaceInode uint64
}

func (m *networkOwnership) setNamespace(info os.FileInfo) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		m.NamespaceDevice = uint64(stat.Dev)
		m.NamespaceInode = stat.Ino
	}
}
func (d Driver) roots() (string, string) {
	root, ns := d.ownershipRoot, d.namespaceRoot
	if root == "" {
		root = "/var/lib/sysbox/networks"
	}
	if ns == "" {
		ns = "/var/run/netns"
	}
	return root, ns
}
func (d Driver) ownershipPath(name string) string {
	root, _ := d.roots()
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(root, fmt.Sprintf("%x.json", sum))
}
func networkAlias(id substrate.OperationIdentity) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", id.OwnerUID, id.TopologyUID, id.ResourceUID, id.Generation)))
	return fmt.Sprintf("sysbox-network:%x", sum)
}
func validateNetworkRequest(ctx context.Context, spec driver.IsolatedNetworkSpec) (substrate.OperationIdentity, error) {
	if err := ctx.Err(); err != nil {
		return substrate.OperationIdentity{}, err
	}
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return id, fmt.Errorf("network mutation/observation requires durable operation identity")
	}
	if err := id.Validate(); err != nil {
		return id, err
	}
	for _, name := range []string{spec.Name, spec.Bridge, spec.RootBridge, spec.RootEnd, spec.NamespaceEnd} {
		if name == "" {
			continue
		}
		for _, c := range name {
			if c <= 32 || c == 127 {
				return id, fmt.Errorf("invalid control character in network name")
			}
		}
		if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return id, fmt.Errorf("invalid network object name")
		}
	}
	if spec.Name == "" || spec.Bridge == "" {
		return id, fmt.Errorf("network namespace and bridge required")
	}
	if spec.RootBridge != "" && (spec.RootEnd == "" || spec.NamespaceEnd == "" || spec.RootEnd == spec.RootBridge || spec.RootEnd == spec.NamespaceEnd || spec.NamespaceEnd == spec.Bridge) {
		return id, fmt.Errorf("distinct complete proxy link names required")
	}
	if spec.RootBridge == "" && (spec.RootEnd != "" || spec.NamespaceEnd != "" || hasRootIngress(spec)) {
		return id, fmt.Errorf("proxy links and ingress require root bridge")
	}
	if hasRootIngress(spec) && spec.RootAddress == "" {
		return id, fmt.Errorf("root ingress requires root address")
	}
	if _, _, err := net.ParseCIDR(spec.CIDR); err != nil {
		return id, fmt.Errorf("invalid network CIDR: %w", err)
	}
	if spec.RootAddress != "" {
		ip, _, err := net.ParseCIDR(spec.RootAddress)
		if err != nil || ip.To4() == nil {
			return id, fmt.Errorf("root address must be IPv4 CIDR")
		}
	}
	for _, route := range spec.RootRoutes {
		ip, _, err := net.ParseCIDR(route.Destination)
		if err != nil || ip.To4() == nil || net.ParseIP(route.Via).To4() == nil {
			return id, fmt.Errorf("root route requires IPv4 destination and gateway")
		}
	}
	return id, nil
}
func missingLink(err error) bool {
	var missing netlink.LinkNotFoundError
	return errors.As(err, &missing) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOENT)
}
func networkQueryError(message string, err error) error {
	category := driver.ErrorUnavailable
	if errors.Is(err, os.ErrPermission) {
		category = driver.ErrorPermissionDenied
	}
	return driver.Wrap(category, "network", message, err)
}
func (d Driver) openNetlink(namespace string) (*netlink.Handle, error) {
	var h *netlink.Handle
	var err error
	if namespace == "" {
		h, err = netlink.NewHandle()
	} else {
		_, root := d.roots()
		ns, openErr := netns.GetFromPath(filepath.Join(root, namespace))
		if openErr != nil {
			return nil, openErr
		}
		defer ns.Close()
		h, err = netlink.NewHandleAt(ns)
	}
	if err != nil {
		return nil, err
	}
	if err := h.SetSocketTimeout(time.Second); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}
func (d Driver) lookupLink(namespace, name string) (netlink.Link, error) {
	if d.linkLookup != nil {
		return d.linkLookup(namespace, name)
	}
	h, err := d.openNetlink(namespace)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return h.LinkByName(name)
}
func (d Driver) addresses(namespace string, link netlink.Link) ([]netlink.Addr, error) {
	if d.addressLookup != nil {
		return d.addressLookup(namespace, link)
	}
	h, err := d.openNetlink(namespace)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return h.AddrList(link, netlink.FAMILY_ALL)
}

func (d Driver) routes(link netlink.Link) ([]netlink.Route, error) {
	if d.routeLookup != nil {
		return d.routeLookup(link)
	}
	h, err := d.openNetlink("")
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return h.RouteList(link, netlink.FAMILY_ALL)
}
func networkRouteMatches(route netlink.Route, destination, via string) bool {
	_, target, err := net.ParseCIDR(destination)
	if err != nil {
		return false
	}
	matches := route.Dst != nil && route.Dst.String() == target.String()
	if route.Dst == nil {
		ones, _ := target.Mask.Size()
		matches = ones == 0
	}
	if !matches {
		return false
	}
	if via == "" {
		return len(route.Gw) == 0 || route.Gw.IsUnspecified()
	}
	return route.Gw.Equal(net.ParseIP(via))
}

func (d Driver) ObserveIsolated(ctx context.Context, spec driver.IsolatedNetworkSpec) (driver.IsolatedNetworkObservation, error) {
	id, err := validateNetworkRequest(ctx, spec)
	if err != nil {
		return driver.IsolatedNetworkObservation{}, err
	}
	_, nsRoot := d.roots()
	nsInfo, err := os.Stat(filepath.Join(nsRoot, spec.Name))
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return driver.IsolatedNetworkObservation{}, networkQueryError("inspect namespace", err)
	}
	type namedLink struct {
		namespace, name, kind string
		link                  netlink.Link
	}
	objects := []namedLink{}
	if exists {
		objects = append(objects, namedLink{namespace: spec.Name, name: spec.Bridge, kind: "bridge"})
		if spec.NamespaceEnd != "" {
			objects = append(objects, namedLink{namespace: spec.Name, name: spec.NamespaceEnd, kind: "veth"})
		}
	}
	if spec.RootBridge != "" {
		objects = append(objects, namedLink{name: spec.RootBridge, kind: "bridge"})
	}
	if spec.RootEnd != "" {
		objects = append(objects, namedLink{name: spec.RootEnd, kind: "veth"})
	}
	present := exists
	healthy := exists
	reason := ""
	for i := range objects {
		if err := ctx.Err(); err != nil {
			return driver.IsolatedNetworkObservation{}, err
		}
		link, err := d.lookupLink(objects[i].namespace, objects[i].name)
		if err != nil {
			if !missingLink(err) {
				return driver.IsolatedNetworkObservation{}, networkQueryError("inspect network link", err)
			}
			healthy = false
			reason = "network link missing"
			continue
		}
		objects[i].link = link
		present = true
	}
	raw, err := os.ReadFile(d.ownershipPath(spec.Name))
	if !present && os.IsNotExist(err) {
		return driver.IsolatedNetworkObservation{}, nil
	}
	if err != nil {
		return driver.IsolatedNetworkObservation{}, networkQueryError("network ownership anchor unavailable", err)
	}
	var marker networkOwnership
	if err := json.Unmarshal(raw, &marker); err != nil {
		return driver.IsolatedNetworkObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "corrupt ownership anchor", err)
	}
	if marker.Version != 2 || marker.Identity.OwnerUID != id.OwnerUID || marker.Identity.TopologyUID != id.TopologyUID || marker.Identity.ResourceUID != id.ResourceUID || marker.Identity.Generation != id.Generation || !reflect.DeepEqual(marker.Spec, spec) {
		return driver.IsolatedNetworkObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "network ownership/config identity conflict", nil)
	}
	if err := validateNetworkLinkProofs(marker); err != nil {
		return driver.IsolatedNetworkObservation{}, err
	}
	for _, proof := range marker.Links {
		if proof.Namespace != "" {
			continue
		}
		rootInfo, err := os.Stat("/proc/self/ns/net")
		if err != nil {
			return driver.IsolatedNetworkObservation{}, networkQueryError("inspect root namespace", err)
		}
		rootStat := rootInfo.Sys().(*syscall.Stat_t)
		if uint64(rootStat.Dev) != proof.Device || rootStat.Ino != proof.Inode {
			return driver.IsolatedNetworkObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "root namespace identity conflict", nil)
		}
	}
	// Physical absence does not authorize replacing another generation's
	// durable anchor. Validate it before reporting absence to Create/Delete.
	if !present {
		return driver.IsolatedNetworkObservation{}, nil
	}
	if exists {
		current := networkOwnership{}
		current.setNamespace(nsInfo)
		if marker.NamespaceInode == 0 || current.NamespaceInode != marker.NamespaceInode || current.NamespaceDevice != marker.NamespaceDevice {
			return driver.IsolatedNetworkObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "namespace identity changed", nil)
		}
	}
	for _, object := range objects {
		if object.link == nil {
			continue
		}
		aliasOK := object.link.Attrs().Alias == networkAlias(id)
		owned := false
		for _, proof := range marker.Links {
			if proof.Namespace == object.namespace && proof.Name == object.name && proof.Index > 0 && proof.Index == object.link.Attrs().Index && proof.Kind == object.kind && proof.Alias == networkAlias(id) {
				owned = true
			}
		}
		if object.link.Type() != object.kind || !aliasOK || !owned {
			return driver.IsolatedNetworkObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", fmt.Sprintf("link ownership/type identity conflict for %s/%s type=%s alias=%q", object.namespace, object.name, object.link.Type(), object.link.Attrs().Alias), nil)
		}
		if object.link.Attrs().Flags&net.FlagUp == 0 {
			healthy = false
			reason = "network link down"
		}
		expected := ""
		if object.namespace == spec.Name && object.name == spec.Bridge {
			expected = spec.CIDR
		}
		if object.namespace == "" && object.name == spec.RootBridge {
			expected = spec.RootAddress
		}
		if expected != "" {
			addresses, err := d.addresses(object.namespace, object.link)
			if err != nil {
				return driver.IsolatedNetworkObservation{}, networkQueryError("inspect link addresses", err)
			}
			found := false
			for _, address := range addresses {
				if address.IPNet != nil && address.IPNet.String() == expected {
					found = true
				}
			}
			if !found {
				healthy = false
				reason = "network address missing"
			}
		}
		if object.namespace == "" && object.name == spec.RootBridge && (spec.RootAddress != "" || len(spec.RootRoutes) > 0) {
			routes, err := d.routes(object.link)
			if err != nil {
				return driver.IsolatedNetworkObservation{}, networkQueryError("inspect root routes", err)
			}
			required := append([]driver.HostRouteSpec{{Destination: spec.CIDR}}, spec.RootRoutes...)
			for _, need := range required {
				found := false
				for _, route := range routes {
					if networkRouteMatches(route, need.Destination, need.Via) {
						found = true
						break
					}
				}
				if !found {
					healthy = false
					reason = "root route missing"
				}
			}
		}
	}
	return driver.IsolatedNetworkObservation{Exists: true, Healthy: healthy, Reason: reason}, nil
}

func (d Driver) writeOwnership(name string, marker networkOwnership) error {
	path := d.ownershipPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".network-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
