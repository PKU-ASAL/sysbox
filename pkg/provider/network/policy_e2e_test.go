//go:build e2e

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestOwnedPolicyRepeatedApplyAndDeleteE2E(t *testing.T) {
	requirePolicyE2E(t)
	namespace := fmt.Sprintf("sysbox-e2e-policy-%d", os.Getpid())
	require.NoError(t, CreateNetns(namespace))
	t.Cleanup(func() { _ = DeleteNetns(namespace) })
	targetRaw, err := json.Marshal(policyTargetState{Namespace: namespace, Bindings: map[string]string{"inside": "lo", "uplink": "lo"}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_firewall.edge", State: targetRaw}
	spec := driver.RulesetSpec{Owner: "e2e/sysbox_firewall.edge", Family: driver.FamilyIPv4,
		NAT: &driver.NATPolicy{SourceAttachment: "inside", UplinkAttachment: "uplink", SourceCIDRs: []string{"127.0.0.0/8"}, Masquerade: true}}
	provider := Driver{}

	first, err := provider.ApplyRuleset(context.Background(), target, spec)
	require.NoError(t, err)
	second, err := provider.ApplyRuleset(context.Background(), target, spec)
	require.NoError(t, err)
	require.Equal(t, first.Table, second.Table)
	require.Equal(t, first.Digest, second.Digest)
	require.Equal(t, len(first.Inventory), len(second.Inventory))
	require.Contains(t, second.Inventory, driver.OwnedObject{Kind: "chain", Name: "postrouting"})

	require.NoError(t, provider.DeleteRuleset(context.Background(), target, spec.Owner))
	_, err = provider.ObserveRuleset(context.Background(), target, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), err)
}

func TestOwnedPolicyDefaultDenyAndStatefulAllowE2E(t *testing.T) {
	requirePolicyE2E(t)
	suffix := os.Getpid() % 100000
	namespace := fmt.Sprintf("sbe2ep%d", suffix)
	clientIf := fmt.Sprintf("sbc%d", suffix)
	serverIf := fmt.Sprintf("sbs%d", suffix)
	require.NoError(t, CreateNetns(namespace))
	t.Cleanup(func() { _ = DeleteNetns(namespace) })
	configurePolicyVeth(t, namespace, clientIf, serverIf)
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(clientIf); err == nil {
			_ = netlink.LinkDel(link)
		}
	})

	var listener net.Listener
	require.NoError(t, inNetns(namespace, func() error {
		var err error
		listener, err = net.Listen("tcp4", "10.254.0.2:0")
		return err
	}))
	t.Cleanup(func() { _ = listener.Close() })
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	targetRaw, err := json.Marshal(policyTargetState{Namespace: namespace, Bindings: map[string]string{}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_firewall.egress", State: targetRaw}
	provider := Driver{}
	owner := "e2e/sysbox_firewall.egress"

	_, err = provider.ApplyRuleset(context.Background(), target, driver.RulesetSpec{Owner: owner, Family: driver.FamilyIPv4})
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 250*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err, "default-drop input/output policy unexpectedly allowed TCP")

	_, err = provider.ApplyRuleset(context.Background(), target, driver.RulesetSpec{
		Owner: owner, Family: driver.FamilyIPv4,
		DefaultInput: driver.VerdictDrop, DefaultOutput: driver.VerdictDrop, DefaultForward: driver.VerdictDrop,
		Rules: []driver.PolicyRule{
			{ID: "allow-new", Direction: driver.DirectionInput, Protocol: driver.ProtocolTCP, DestinationPorts: []driver.PortRange{{From: port, To: port}}, States: []driver.ConnectionState{driver.StateNew}, Verdict: driver.VerdictAccept},
			{ID: "allow-return", Direction: driver.DirectionOutput, Protocol: driver.ProtocolTCP, States: []driver.ConnectionState{driver.StateEstablished, driver.StateRelated}, Verdict: driver.VerdictAccept},
		},
	})
	require.NoError(t, err)
	conn, err = net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.NoError(t, provider.DeleteRuleset(context.Background(), target, owner))
}

func TestOwnedPolicyForwardDefaultDenyAndStatefulAllowE2E(t *testing.T) {
	requirePolicyE2E(t)
	suffix := os.Getpid() % 10000
	routerNS := fmt.Sprintf("sbr%d", suffix)
	sourceNS := fmt.Sprintf("sbs%d", suffix)
	destNS := fmt.Sprintf("sbd%d", suffix)
	for _, namespace := range []string{routerNS, sourceNS, destNS} {
		require.NoError(t, CreateNetns(namespace))
		ns := namespace
		t.Cleanup(func() { _ = DeleteNetns(ns) })
	}
	configureRoutedPair(t, routerNS, sourceNS, "rin", "src", "10.253.1.1/30", "10.253.1.2/30", "10.253.2.0/30", "10.253.1.1")
	configureRoutedPair(t, routerNS, destNS, "rout", "dst", "10.253.2.1/30", "10.253.2.2/30", "10.253.1.0/30", "10.253.2.1")
	require.NoError(t, inNetns(routerNS, func() error { return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644) }))

	var listener net.Listener
	require.NoError(t, inNetns(destNS, func() error {
		var err error
		listener, err = net.Listen("tcp4", "10.253.2.2:0")
		return err
	}))
	t.Cleanup(func() { _ = listener.Close() })
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	targetRaw, err := json.Marshal(policyTargetState{Namespace: routerNS, Bindings: map[string]string{"inside": "rin", "uplink": "rout"}})
	require.NoError(t, err)
	target := driver.PolicyTarget{Resource: "sysbox_router.forward", State: targetRaw}
	provider := Driver{}
	owner := "e2e/sysbox_router.forward"

	_, err = provider.ApplyRuleset(context.Background(), target, driver.RulesetSpec{Owner: owner, Family: driver.FamilyIPv4, DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop})
	require.NoError(t, err)
	require.Error(t, dialFromNamespace(sourceNS, listener.Addr().String(), 250*time.Millisecond), "default-drop forward policy unexpectedly allowed TCP")

	_, err = provider.ApplyRuleset(context.Background(), target, driver.RulesetSpec{
		Owner: owner, Family: driver.FamilyIPv4, DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
		Rules: []driver.PolicyRule{
			{ID: "allow-new", Direction: driver.DirectionForward, Protocol: driver.ProtocolTCP, DestinationPorts: []driver.PortRange{{From: port, To: port}}, InputAttachment: "inside", OutputAttachment: "uplink", States: []driver.ConnectionState{driver.StateNew}, Verdict: driver.VerdictAccept},
			{ID: "allow-return", Direction: driver.DirectionForward, Protocol: driver.ProtocolTCP, InputAttachment: "uplink", OutputAttachment: "inside", States: []driver.ConnectionState{driver.StateEstablished, driver.StateRelated}, Verdict: driver.VerdictAccept},
		},
	})
	require.NoError(t, err)
	require.NoError(t, dialFromNamespace(sourceNS, listener.Addr().String(), time.Second))
	require.NoError(t, provider.DeleteRuleset(context.Background(), target, owner))
}

func configureRoutedPair(t *testing.T, routerNS, endpointNS, routerIf, endpointIf, routerCIDR, endpointCIDR, routeCIDR, gateway string) {
	t.Helper()
	attrs := netlink.NewLinkAttrs()
	attrs.Name = routerIf
	require.NoError(t, netlink.LinkAdd(&netlink.Veth{LinkAttrs: attrs, PeerName: endpointIf}))
	routerLink, err := netlink.LinkByName(routerIf)
	require.NoError(t, err)
	endpointLink, err := netlink.LinkByName(endpointIf)
	require.NoError(t, err)
	routerHandle, err := netns.GetFromName(routerNS)
	require.NoError(t, err)
	defer routerHandle.Close()
	endpointHandle, err := netns.GetFromName(endpointNS)
	require.NoError(t, err)
	defer endpointHandle.Close()
	require.NoError(t, netlink.LinkSetNsFd(routerLink, int(routerHandle)))
	require.NoError(t, netlink.LinkSetNsFd(endpointLink, int(endpointHandle)))
	require.NoError(t, configureNamespacedLink(routerNS, routerIf, routerCIDR, "", ""))
	require.NoError(t, configureNamespacedLink(endpointNS, endpointIf, endpointCIDR, routeCIDR, gateway))
}

func configureNamespacedLink(namespace, name, cidr, routeCIDR, gateway string) error {
	return inNetns(namespace, func() error {
		link, err := netlink.LinkByName(name)
		if err != nil {
			return err
		}
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(link, address); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return err
		}
		loopback, err := netlink.LinkByName("lo")
		if err != nil {
			return err
		}
		if err := netlink.LinkSetUp(loopback); err != nil {
			return err
		}
		if routeCIDR == "" {
			return nil
		}
		_, destination, err := net.ParseCIDR(routeCIDR)
		if err != nil {
			return err
		}
		return netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Gw: net.ParseIP(gateway)})
	})
}

func dialFromNamespace(namespace, address string, timeout time.Duration) error {
	return inNetns(namespace, func() error {
		conn, err := net.DialTimeout("tcp4", address, timeout)
		if err != nil {
			return err
		}
		return conn.Close()
	})
}

func configurePolicyVeth(t *testing.T, namespace, clientIf, serverIf string) {
	t.Helper()
	attrs := netlink.NewLinkAttrs()
	attrs.Name = clientIf
	require.NoError(t, netlink.LinkAdd(&netlink.Veth{LinkAttrs: attrs, PeerName: serverIf}))
	client, err := netlink.LinkByName(clientIf)
	require.NoError(t, err)
	peer, err := netlink.LinkByName(serverIf)
	require.NoError(t, err)
	target, err := netns.GetFromName(namespace)
	require.NoError(t, err)
	defer target.Close()
	require.NoError(t, netlink.LinkSetNsFd(peer, int(target)))
	address, err := netlink.ParseAddr("10.254.0.1/30")
	require.NoError(t, err)
	require.NoError(t, netlink.AddrAdd(client, address))
	require.NoError(t, netlink.LinkSetUp(client))
	require.NoError(t, inNetns(namespace, func() error {
		server, err := netlink.LinkByName(serverIf)
		if err != nil {
			return err
		}
		address, err := netlink.ParseAddr("10.254.0.2/30")
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(server, address); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(server); err != nil {
			return err
		}
		loopback, err := netlink.LinkByName("lo")
		if err != nil {
			return err
		}
		return netlink.LinkSetUp(loopback)
	}))
}

func requirePolicyE2E(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("owned nftables policy e2e requires root/CAP_NET_ADMIN")
	}
	probe := fmt.Sprintf("sysbox-e2e-policy-cap-%d", os.Getpid())
	if err := CreateNetns(probe); err != nil {
		t.Skipf("owned nftables policy e2e requires CAP_NET_ADMIN: %v", err)
	}
	require.NoError(t, DeleteNetns(probe))
}

func TestRootBridgeProxyLifecycleE2E(t *testing.T) {
	requirePolicyE2E(t)
	suffix := fmt.Sprintf("%x", os.Getpid())
	spec := driver.IsolatedNetworkSpec{
		Name:         "sbpx-" + suffix,
		Bridge:       "bpx" + suffix,
		CIDR:         "10.253.0.1/24",
		RootBridge:   "lvx" + suffix,
		RootEnd:      "lvr" + suffix,
		NamespaceEnd: "lvn" + suffix,
	}
	provider := Driver{}
	t.Cleanup(func() { _ = provider.DeleteIsolated(context.Background(), spec) })

	require.NoError(t, provider.CreateIsolated(context.Background(), spec))
	ok, reason := provider.NetworkHealthy(context.Background(), spec)
	require.True(t, ok, reason)
	require.True(t, LinkExists(spec.Name, spec.NamespaceEnd))
	_, err := netlink.LinkByName(spec.RootBridge)
	require.NoError(t, err)

	require.NoError(t, provider.DeleteIsolated(context.Background(), spec))
	require.False(t, NetnsExists(spec.Name))
	_, err = netlink.LinkByName(spec.RootBridge)
	require.Error(t, err)
	_, err = netlink.LinkByName(spec.RootEnd)
	require.Error(t, err)
}
