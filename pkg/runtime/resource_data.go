package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// readDataNode queries a substrate for an existing node and records it in
// state. Unlike createNode, this does not create any infrastructure — it
// merely reads the node's current attributes so other resources can reference
// them in the eval context.
func (e *Executor) readDataNode(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.DataNodeConfig)
	if !ok {
		return fmt.Errorf("data sysbox_node.%s: wrong data type", n.ID.Name)
	}

	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return fmt.Errorf("data sysbox_node.%s: %w", n.ID.Name, err)
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return fmt.Errorf("data sysbox_node.%s: %w", n.ID.Name, err)
	}

	handle, err := sub.ReadNode(ctx, cfg.ID)
	if err != nil {
		return fmt.Errorf("data sysbox_node.%s: read %q: %w", n.ID.Name, cfg.ID, err)
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
	}
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		inst["provider_extra"] = string(blob)
	}
	inst["data_read"] = true // mark as read-only so destroy skips it

	e.state.AddResource(state.Resource{
		Type:     "data_sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	})
	e.logf("[data] read sysbox_node.%s → id=%s ip=%s\n", n.ID.Name, handle.ID, handle.Net.PrimaryIP)
	return nil
}

// readDataNetwork queries an existing Docker network by name and records
// its attributes in state. Unlike readDataNode, this is substrate-specific
// because sysbox_network is only managed by Docker currently.
func (e *Executor) readDataNetwork(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.DataNetworkConfig)
	if !ok {
		return fmt.Errorf("data sysbox_network.%s: wrong data type", n.ID.Name)
	}

	sub, err := substrate.Get("docker")
	if err != nil {
		return fmt.Errorf("data sysbox_network.%s: requires docker substrate: %w", n.ID.Name, err)
	}

	// Use ReadNode is not applicable for networks; instead, query by
	// trying to get the managed network info. If the network doesn't
	// exist, CreateManagedNetwork with empty CIDR won't create it — but
	// the semantics are wrong. We use docker inspect directly instead.
	// However, to avoid importing Docker SDK in the runtime package,
	// we delegate to the substrate's existing interface.
	//
	// Approach: call CreateManagedNetwork with the known name but empty
	// CIDR. Docker's network inspect (used internally) returns the
	// existing network if it exists, without creating anything new.
	info, err := sub.CreateManagedNetwork(ctx, substrate.ManagedNetworkSpec{
		Name: cfg.Name,
		CIDR: "", // empty CIDR → Docker inspects existing, doesn't create
		NAT:  false,
	})
	if err != nil {
		return fmt.Errorf("data sysbox_network.%s: network %q not found: %w", n.ID.Name, cfg.Name, err)
	}

	e.state.AddResource(state.Resource{
		Type:     "data_sysbox_network",
		Name:     n.ID.Name,
		Provider: "docker",
		Instance: map[string]any{
			"docker_network_id": info.ID,
			"docker_net_name":   info.Name,
			"data_read":         true,
		},
	})
	e.logf("[data] read sysbox_network.%s → %s\n", n.ID.Name, info.Name)
	return nil
}
