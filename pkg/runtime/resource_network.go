package runtime

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	createNetnsFn  = network.CreateNetns
	deleteNetnsFn  = network.DeleteNetns
	createBridgeFn = network.CreateBridge
	deleteBridgeFn = network.DeleteBridge
)

type NetworkResourceProvider struct{}

func init() {
	RegisterResourceProvider(NetworkResourceProvider{})
}

func (NetworkResourceProvider) Type() string { return "sysbox_network" }

func (NetworkResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_network")
}

func (NetworkResourceProvider) Read(_ context.Context, current state.Resource) (ResourceReadResult, error) {
	result := resourceReadOK(current)
	if current.IsNAT() {
		result.Checks = map[string]controlplane.ResourceCheckHealth{"docker_network": {OK: true}}
		return result, nil
	}
	nsName := current.Str("netns")
	brName := current.Str("bridge")
	if nsName == "" {
		result.Reason = "network has no isolated namespace"
		return result, nil
	}
	checks := map[string]controlplane.ResourceCheckHealth{"netns": {OK: network.NetnsExists(nsName)}}
	if !network.NetnsExists(nsName) {
		checks["netns"] = controlplane.ResourceCheckHealth{OK: false, Reason: "network namespace missing"}
		result.Checks = checks
		return result, driftedResource("network namespace missing")
	}
	if brName != "" && !network.BridgeExists(nsName, brName) {
		checks["bridge"] = controlplane.ResourceCheckHealth{OK: false, Reason: "bridge missing"}
		result.Checks = checks
		return result, driftedResource("bridge missing")
	}
	if brName != "" {
		checks["bridge"] = controlplane.ResourceCheckHealth{OK: true}
	}
	result.Checks = checks
	return result, nil
}

func (NetworkResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (NetworkResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("network %s: wrong data type", n.ID)
	}

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return createNATNetwork(ctx, pc, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	networkName := networkExternalName(pc.Topology(), n.ID.Name)
	nsName := fmt.Sprintf("sysbox-net-%s", networkName)
	if err := createNetnsFn(nsName); err != nil {
		return state.Resource{}, err
	}

	brName := shortLinuxName("br", networkName)
	gwCIDR, err := network.GatewayCIDR(cfg.CIDR)
	if err != nil {
		return state.Resource{}, err
	}
	if err := createBridgeFn(network.BridgeConfig{
		NetnsName: nsName, BridgeName: brName, CIDR: gwCIDR,
	}); err != nil {
		return state.Resource{}, err
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
		return state.Resource{}, err
	}
	return state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: inst,
	}, nil
}

// createNATNetwork creates a managed NAT network via the registered substrate.
// Currently Docker is the only substrate that supports managed networks.
func createNATNetwork(ctx context.Context, pc *ProviderContext, n *graph.Node, cfg *config.NetworkConfig) (state.Resource, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	info, err := sub.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		Name:   networkExternalName(pc.Topology(), n.ID.Name),
		CIDR:   cfg.CIDR,
		NAT:    true,
		Labels: ManagedLabels(pc.Topology(), pc.RunID(), n.ID),
	})
	if err != nil {
		return state.Resource{}, fmt.Errorf("create nat network %s: %w", n.ID.Name, err)
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
		return state.Resource{}, err
	}
	return state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: natInst,
	}, nil
}

func (p NetworkResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (NetworkResourceProvider) Delete(ctx context.Context, pc *ProviderContext, r state.Resource) error {
	if r.IsNAT() {
		sub, err := substrate.Get("docker")
		if err != nil {
			pc.State().RemoveResource(r.Type, r.Name)
			return nil
		}
		netID := r.DockerNetID()
		if netID != "" {
			if err := sub.RemoveManagedNetwork(ctx, netID); err != nil {
				pc.Logf("[destroy] warning: remove bridge network %s: %v\n", netID, err)
			}
		}
		// Clean up DOCKER-USER ACCEPT rules for this NAT subnet.
		cidr := r.Str("cidr")
		if cidr != "" {
			_ = removeDockerUserAccept(cidr)
		}
		pc.State().RemoveResource(r.Type, r.Name)
		return nil
	}

	nsName := r.Str("netns")
	brName := r.Str("bridge")
	if err := deleteBridgeFn(network.BridgeConfig{NetnsName: nsName, BridgeName: brName}); err != nil {
		pc.Logf("[destroy] warning: delete bridge %s: %v\n", brName, err)
	}
	if err := deleteNetnsFn(nsName); err != nil {
		pc.Logf("[destroy] warning: delete netns %s: %v\n", nsName, err)
	}
	pc.State().RemoveResource(r.Type, r.Name)
	return nil
}

func (NetworkResourceProvider) ExternalID(current state.Resource) string {
	if id := current.DockerNetID(); id != "" {
		return id
	}
	if ns := current.NetNS(); ns != "" {
		return ns
	}
	return current.Str("id")
}

func (NetworkResourceProvider) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.NetworkConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	return cfg, nil, nil
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
