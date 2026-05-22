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

var (
	createNetnsFn  = network.CreateNetns
	deleteNetnsFn  = network.DeleteNetns
	createBridgeFn = network.CreateBridge
	deleteBridgeFn = network.DeleteBridge
)

func (e *Executor) createNetwork(ctx context.Context, n *graph.Node) error {
	p := mustResourceProvider("sysbox_network")
	res, err := p.Create(ctx, e, n)
	if err != nil {
		return err
	}
	e.state.AddResource(res)
	return nil
}

type NetworkResourceProvider struct{}

func init() {
	RegisterResourceProvider(NetworkResourceProvider{})
}

func (NetworkResourceProvider) Type() string { return "sysbox_network" }

func (NetworkResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_network")
}

func (NetworkResourceProvider) Read(_ context.Context, current state.Resource) (state.Resource, error) {
	return current, nil
}

func (NetworkResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	if current == nil {
		return PlanAction{
			Resource: desired.ID.String(),
			Type:     desired.ID.Type,
			Name:     desired.ID.Name,
			Action:   PlanActionCreate,
			Reason:   "resource not present in state",
		}, nil
	}
	action := PlanActionNoop
	reason := ""
	var changes map[string]FieldChange
	if stateDesiredHash(current) != "" {
		want, err := desiredHash(desired)
		if err != nil {
			return PlanAction{}, err
		}
		if want != stateDesiredHash(current) {
			changes, reason = diffDesiredState(desired, current)
			action = PlanActionReplace
		}
	}
	if action == PlanActionReplace && reason == "" {
		reason = "desired configuration changed; replacement required"
	}
	return PlanAction{
		Resource: desired.ID.String(),
		Type:     desired.ID.Type,
		Name:     desired.ID.Name,
		Action:   action,
		Reason:   reason,
		Changes:  changes,
	}, nil
}

func (NetworkResourceProvider) Create(ctx context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("network %s: wrong data type", n.ID)
	}

	// nat=true: use Docker's bridge driver for internet access.
	if cfg.NAT {
		return createNATNetwork(ctx, exec, n, cfg)
	}

	// Default: isolated netns/bridge/veth topology.
	nsName := fmt.Sprintf("sysbox-net-%s", n.ID.Name)
	if err := createNetnsFn(nsName); err != nil {
		return state.Resource{}, err
	}

	brName := fmt.Sprintf("br-%s", n.ID.Name)
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
func createNATNetwork(ctx context.Context, exec *Executor, n *graph.Node, cfg *config.NetworkConfig) (state.Resource, error) {
	sub, err := substrate.Get("docker")
	if err != nil {
		return state.Resource{}, fmt.Errorf("nat network requires docker substrate: %w", err)
	}

	info, err := sub.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		Name:   n.ID.Name,
		CIDR:   cfg.CIDR,
		NAT:    true,
		Labels: ManagedLabels(exec.topology, exec.runID, n.ID),
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

func (p NetworkResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (NetworkResourceProvider) Delete(ctx context.Context, exec *Executor, r state.Resource) error {
	if r.IsNAT() {
		sub, err := substrate.Get("docker")
		if err != nil {
			exec.state.RemoveResource(r.Type, r.Name)
			return nil
		}
		netID := r.DockerNetID()
		if netID != "" {
			if err := sub.RemoveManagedNetwork(ctx, netID); err != nil {
				exec.logf("[destroy] warning: remove bridge network %s: %v\n", netID, err)
			}
		}
		// Clean up DOCKER-USER ACCEPT rules for this NAT subnet.
		cidr := r.Str("cidr")
		if cidr != "" {
			_ = removeDockerUserAccept(cidr)
		}
		exec.state.RemoveResource(r.Type, r.Name)
		return nil
	}

	nsName := r.Str("netns")
	brName := r.Str("bridge")
	if err := deleteBridgeFn(network.BridgeConfig{NetnsName: nsName, BridgeName: brName}); err != nil {
		exec.logf("[destroy] warning: delete bridge %s: %v\n", brName, err)
	}
	if err := deleteNetnsFn(nsName); err != nil {
		exec.logf("[destroy] warning: delete netns %s: %v\n", nsName, err)
	}
	exec.state.RemoveResource(r.Type, r.Name)
	return nil
}

func (e *Executor) destroyNetwork(ctx context.Context, r state.Resource) error {
	p := mustResourceProvider("sysbox_network")
	return p.Delete(ctx, e, r)
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
