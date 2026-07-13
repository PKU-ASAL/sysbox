package network

import (
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type BridgeConfig struct {
	NetnsName  string
	BridgeName string
	CIDR       string // gateway IP in CIDR form (e.g. "10.0.1.1/24")
}

// CreateBridge builds a Linux bridge inside the named netns and assigns it
// an IP (acts as the gateway for nodes attached to the network).
// Idempotent: if the bridge already exists, it is reused.
func CreateBridge(cfg BridgeConfig) error {
	return inNetns(cfg.NetnsName, func() error {
		// Check if bridge already exists.
		if existing, err := netlink.LinkByName(cfg.BridgeName); err == nil {
			// Bridge exists — ensure it's up and has the IP.
			_ = netlink.LinkSetUp(existing)
			addr, _ := netlink.ParseAddr(cfg.CIDR)
			if addr != nil {
				_ = netlink.AddrAdd(existing, addr) // ignore "file exists" for IP
			}
			return nil
		}

		la := netlink.NewLinkAttrs()
		la.Name = cfg.BridgeName

		br := &netlink.Bridge{LinkAttrs: la}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("add bridge %s: %w", cfg.BridgeName, err)
		}

		addr, err := netlink.ParseAddr(cfg.CIDR)
		if err != nil {
			return fmt.Errorf("parse CIDR %s: %w", cfg.CIDR, err)
		}
		if err := netlink.AddrAdd(br, addr); err != nil {
			return fmt.Errorf("add IP to bridge: %w", err)
		}

		return netlink.LinkSetUp(br)
	})
}

func DeleteBridge(cfg BridgeConfig) error {
	return inNetns(cfg.NetnsName, func() error {
		link, err := netlink.LinkByName(cfg.BridgeName)
		if err != nil {
			return nil
		}
		return netlink.LinkDel(link)
	})
}

func BridgeExists(nsName, brName string) bool {
	exists := false
	_ = inNetns(nsName, func() error {
		if _, err := netlink.LinkByName(brName); err == nil {
			exists = true
		}
		return nil
	})
	return exists
}

func LinkExists(nsName, linkName string) bool {
	if nsName == "" || linkName == "" {
		return false
	}
	exists := false
	_ = inNetns(nsName, func() error {
		if _, err := netlink.LinkByName(linkName); err == nil {
			exists = true
		}
		return nil
	})
	return exists
}

// inNetns runs fn inside the named netns and switches back.
// Uses runtime.LockOSThread so the netns switch doesn't leak to other goroutines.
func inNetns(name string, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return err
	}
	defer orig.Close()

	ns, err := netns.GetFromName(name)
	if err != nil {
		return fmt.Errorf("get netns %s: %w", name, err)
	}
	defer ns.Close()

	if err := netns.Set(ns); err != nil {
		return fmt.Errorf("enter netns %s: %w", name, err)
	}
	defer func() { _ = netns.Set(orig) }()

	return fn()
}

// GatewayCIDR takes "10.0.1.0/24" and returns "10.0.1.1/24" (first usable host).
func GatewayCIDR(cidr string) (string, error) {
	parts := strings.SplitN(cidr, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("not a CIDR: %s", cidr)
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ip := make(net.IP, len(ipnet.IP))
	copy(ip, ipnet.IP)
	if v4 := ip.To4(); v4 != nil {
		v4[3]++
		return v4.String() + "/" + parts[1], nil
	}
	ip[len(ip)-1]++
	return ip.String() + "/" + parts[1], nil
}
