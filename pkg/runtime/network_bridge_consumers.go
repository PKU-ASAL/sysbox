package runtime

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func inferManagedBridgeExposure(g *graph.Graph, st *state.State) {
	for _, node := range g.All() {
		cfg, ok := node.Data.(*config.NetworkConfig)
		if !ok || !cfg.NAT {
			continue
		}
		cfg.ExposeBridge = requiresManagedBridge(g, node.Address)
		if current := st.FindResource(node.Address); hasManagedBridge(current) {
			cfg.ExposeBridge = true
		}
	}
}

func (e *Executor) preflightManagedBridges(ctx context.Context) error {
	if e.graph == nil {
		return nil
	}
	for _, node := range e.graph.All() {
		cfg, ok := node.Data.(*config.NetworkConfig)
		if !ok || !cfg.NAT || !requiresManagedBridge(e.graph, node.Address) {
			continue
		}
		network, err := driver.DefaultRegistry.RequireNetwork("docker")
		if err != nil {
			return err
		}
		bridge, ok := network.(driver.ManagedBridgeVerifier)
		if !ok {
			return fmt.Errorf("network %s requires managed bridge capability", node.Address)
		}
		if err := bridge.PreflightManagedBridge(ctx); err != nil {
			return err
		}
		if current := e.state.FindResource(node.Address); current != nil && !hasManagedBridge(current) {
			for _, resource := range e.state.Resources {
				for _, attachment := range resource.Attachments {
					if attachment.Network.Equal(node.Address) {
						return fmt.Errorf("network %s needs explicit bridge exposure replacement; remove existing attached nodes before rebuilding the network", node.Address)
					}
				}
			}
		}
	}
	return nil
}

func hasManagedBridge(resource *state.Resource) bool {
	if resource == nil {
		return false
	}
	private, err := resource.RuntimeState()
	return err == nil && private["managed_bridge"] != nil
}

func requiresManagedBridge(g *graph.Graph, target address.Address) bool {
	if g == nil {
		return false
	}
	for _, node := range g.All() {
		var provider string
		var refs []string
		switch cfg := node.Data.(type) {
		case *config.NodeConfig:
			provider = cfg.Substrate
			for _, link := range cfg.Links {
				refs = append(refs, link.Network)
			}
		case *config.RouterConfig:
			provider = cfg.Substrate
			for _, link := range cfg.Interfaces {
				refs = append(refs, link.Network)
			}
		default:
			continue
		}
		name, err := resolveSubstrateRef(provider)
		if err != nil || name == "docker" {
			continue
		}
		for _, ref := range refs {
			addr, err := config.ResolveResourceAddress(ref, "sysbox_network")
			if err == nil && addr.Equal(target) {
				return true
			}
		}
	}
	return false
}
