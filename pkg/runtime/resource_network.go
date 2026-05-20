package runtime

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (e *Executor) createNetwork(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return fmt.Errorf("network %s: wrong data type", n.ID)
	}

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return e.createNATNetwork(ctx, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	nsName := fmt.Sprintf("sysbox-net-%s", n.ID.Name)
	if err := network.CreateNetns(nsName); err != nil {
		return err
	}

	brName := fmt.Sprintf("br-%s", n.ID.Name)
	gwCIDR, err := network.GatewayCIDR(cfg.CIDR)
	if err != nil {
		return err
	}
	if err := network.CreateBridge(network.BridgeConfig{
		NetnsName: nsName, BridgeName: brName, CIDR: gwCIDR,
	}); err != nil {
		return err
	}

	inst := map[string]any{
		"netns":   nsName,
		"bridge":  brName,
		"cidr":    cfg.CIDR,
		"gateway": gwCIDR,
	}
	if lc := cfg.Lifecycle; lc != nil {
		inst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, inst); err != nil {
		return err
	}
	e.state.AddResource(state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: inst,
	})
	return nil
}

// createNATNetwork creates a managed NAT network via the registered substrate.
// Currently Docker is the only substrate that supports managed networks.
func (e *Executor) createNATNetwork(ctx context.Context, n *graph.Node, cfg *config.NetworkConfig) error {
	sub, err := substrate.Get("docker")
	if err != nil {
		return fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	info, err := sub.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		Name: n.ID.Name,
		CIDR: cfg.CIDR,
		NAT:  true,
	})
	if err != nil {
		return fmt.Errorf("create nat network %s: %w", n.ID.Name, err)
	}

	// Ensure Docker containers on this NAT network can reach the internet.
	// Some hosts have restrictive DOCKER-USER iptables policies (e.g. campus
	// networks that DROP outbound HTTP). Insert ACCEPT rules for the NAT
	// subnet before any DROP rules so container traffic is allowed.
	if err := ensureDockerUserAccept(cfg.CIDR); err != nil {
		fmt.Printf("[network %s] warning: could not add DOCKER-USER ACCEPT for %s: %v\n",
			n.ID.Name, cfg.CIDR, err)
	}

	natInst := map[string]any{
		"nat":               true,
		"docker_network_id": info.ID,
		"docker_net_name":   info.Name,
		"cidr":              cfg.CIDR,
	}
	if lc := cfg.Lifecycle; lc != nil {
		natInst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, natInst); err != nil {
		return err
	}
	e.state.AddResource(state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: natInst,
	})
	return nil
}

func (e *Executor) destroyNetwork(ctx context.Context, r state.Resource) error {
	if r.IsNAT() {
		sub, err := substrate.Get("docker")
		if err != nil {
			e.state.RemoveResource(r.Type, r.Name)
			return nil
		}
		netID := r.DockerNetID()
		if netID != "" {
			if err := sub.RemoveManagedNetwork(ctx, netID); err != nil {
				e.logf("[destroy] warning: remove bridge network %s: %v\n", netID, err)
			}
		}
		// Clean up DOCKER-USER ACCEPT rules for this NAT subnet.
		cidr := r.Str("cidr")
		if cidr != "" {
			_ = removeDockerUserAccept(cidr)
		}
		e.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	nsName := r.Str("netns")
	brName := r.Str("bridge")
	if err := network.DeleteBridge(network.BridgeConfig{NetnsName: nsName, BridgeName: brName}); err != nil {
		e.logf("[destroy] warning: delete bridge %s: %v\n", brName, err)
	}
	if err := network.DeleteNetns(nsName); err != nil {
		e.logf("[destroy] warning: delete netns %s: %v\n", nsName, err)
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// ensureDockerUserAccept inserts ACCEPT rules into the DOCKER-USER iptables
// chain for the given NAT subnet, allowing TCP ports 80 (HTTP) and 443 (HTTPS)
// outbound. This is needed on hosts where a restrictive DOCKER-USER policy
// (e.g. DROP tcp dpt:80) would otherwise block container internet access.
//
// The rules are inserted at position 1 (before any DROP rules) and are
// idempotent — duplicate insertions are ignored.
func ensureDockerUserAccept(cidr string) error {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid cidr %q: %w", cidr, err)
	}
	src := ipNet.String()
	for _, port := range []string{"80", "443"} {
		// -C: check if rule already exists (idempotent).
		args := []string{"-C", "DOCKER-USER", "-p", "tcp", "--dport", port, "-s", src, "-j", "ACCEPT"}
		if _, err := iptablesCmd(args...); err == nil {
			continue // rule already present
		}
		// -I 1: insert at position 1 (before any DROP rules).
		args = []string{"-I", "DOCKER-USER", "1", "-p", "tcp", "--dport", port, "-s", src, "-j", "ACCEPT"}
		if out, err := iptablesCmd(args...); err != nil {
			return fmt.Errorf("iptables %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// removeDockerUserAccept deletes the ACCEPT rules previously added by
// ensureDockerUserAccept for the given NAT subnet.
func removeDockerUserAccept(cidr string) error {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	src := ipNet.String()
	for _, port := range []string{"80", "443"} {
		args := []string{"-D", "DOCKER-USER", "-p", "tcp", "--dport", port, "-s", src, "-j", "ACCEPT"}
		_, _ = iptablesCmd(args...)
	}
	return nil
}

// iptablesCmd runs an iptables command. Extracted for testability.
var iptablesCmd = func(args ...string) ([]byte, error) {
	return exec.Command("iptables", args...).CombinedOutput()
}
