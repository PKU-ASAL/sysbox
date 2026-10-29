package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// runAssignedCommand binds every dispatch, including outbox repair, to the
// canonical topology identity. Serial zero is the valid initial state.
func (s *WorkspaceService) runAssignedCommand(ctx context.Context, run *controlplane.Run) (controlplane.AgentCommand, error) {
	if s == nil || s.stateManager == nil {
		return controlplane.AgentCommand{}, fmt.Errorf("canonical topology state is unavailable")
	}
	manager, err := s.stateManager(run.Topology)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	canonical, err := manager.LoadWithContext(ctx)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	if canonical == nil || canonical.TopologyUID == "" || canonical.Meta.Serial < 0 {
		return controlplane.AgentCommand{}, fmt.Errorf("canonical topology state is unavailable")
	}
	command := controlplaneRunAssignedCommand(run)
	command.TopologyUID = canonical.TopologyUID
	command.ActionID = run.ID
	command.ExpectedSerial = canonical.Meta.Serial
	if run.Target != "" {
		target, err := address.Parse(run.Target)
		if err != nil || !target.Equal(address.Resource(target.Type, target.Name)) || run.Op != "apply" && target.Type != "sysbox_node" {
			return controlplane.AgentCommand{}, fmt.Errorf("invalid remote Run target")
		}
		resource := canonical.FindResource(target)
		if run.Op == "apply" && (resource == nil || target.Type != "sysbox_node") {
			// A first creation has no resource identity yet. The signed request
			// still binds its exact target, topology and expected state serial.
		} else if resource == nil || resource.OwnerUID != canonical.TopologyUID || resource.ResourceUID == "" || resource.Generation < 1 {
			return controlplane.AgentCommand{}, fmt.Errorf("remote Run target has no canonical node identity: %s", run.Target)
		} else {
			command.ResourceUID, command.Generation = resource.ResourceUID, resource.Generation
		}
	}
	command.ID = runCommandID(*run)
	command.AgentID = run.AgentID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	return command, nil
}

func controlplaneRunAssignedCommand(run *controlplane.Run) controlplane.AgentCommand {
	rec := runRecord(*run)
	// Sensitive apply inputs must not enter the durable command store. The agent
	// binds var.<name> from the claim response (which re-attaches the in-memory
	// inputs), so the command's Run does not need them.
	rec.Inputs = nil
	return controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  ptrRun(rec),
	}
}

func ptrRun(run controlplane.Run) *controlplane.Run { return &run }

// substrateProbe decodes only the substrate field the capability prescan cares
// about; everything else (including var.<name> references like cidr/env) lands
// in Remain unevaluated, so the prescan never fails on a var it doesn't need.
type substrateProbe struct {
	Substrate string   `hcl:"substrate"`
	Remain    hcl.Body `hcl:",remain"`
}

// networkProbe decodes only the NAT field the capability prescan cares about.
type networkProbe struct {
	NAT    bool     `hcl:"nat,optional"`
	Remain hcl.Body `hcl:",remain"`
}

func requiredCapabilitiesForTopology(path string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx, err := config.BuildEvalContext(root)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range root.Resources {
		switch r.Type {
		case "sysbox_node", "sysbox_router", "sysbox_image", "sysbox_kernel":
			probe := &substrateProbe{}
			if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
				return nil, err
			}
			addSubstrateCapabilities(set, probe.Substrate)
		case "sysbox_network":
			probe := &networkProbe{}
			if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
				return nil, err
			}
			if !probe.NAT {
				set["network"] = true
			}
		case "sysbox_firewall":
			set["network"] = true
		}
	}
	return capabilitiesFromSet(set), nil
}

func requiredCapabilitiesForNode(path, node string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx, err := config.BuildEvalContext(root)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range root.Resources {
		if r.Name != node || (r.Type != "sysbox_node" && r.Type != "sysbox_router") {
			continue
		}
		probe := &substrateProbe{}
		if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
			return nil, err
		}
		addSubstrateCapabilities(set, probe.Substrate)
		return capabilitiesFromSet(set), nil
	}
	return nil, fmt.Errorf("node %q not found in topology", node)
}

func addSubstrateCapabilities(set map[string]bool, substrateName string) {
	// Prefer the registered substrate's self-declared capabilities so adding
	// a new substrate does not require editing this function.
	if nodeDriver, err := driver.DefaultRegistry.RequireNode(substrateName); err == nil {
		for _, cap := range substrate.CapabilityNames(substrateName, nodeDriver.Capabilities()) {
			set[cap] = true
		}
		return
	}
	// Fallback for unregistered substrate identifiers (HCL aliases, etc.).
	switch substrateName {
	case "", "docker":
		set["docker"] = true
	case "firecracker", "microvm":
		set["firecracker"] = true
		set["kvm"] = true
		set["network"] = true
	case "libvirt", "vm":
		set["libvirt"] = true
		set["kvm"] = true
		set["network"] = true
	case "network":
		set["network"] = true
	default:
		set[substrateName] = true
	}
}

func normalizeCapabilities(in []string) []string {
	set := map[string]bool{}
	for _, cap := range in {
		if cap != "" {
			set[cap] = true
		}
	}
	return capabilitiesFromSet(set)
}

func capabilitiesFromSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for cap := range set {
		out = append(out, cap)
	}
	sort.Strings(out)
	return out
}

func hasCapabilities(have, required []string) bool {
	set := map[string]bool{}
	for _, cap := range have {
		set[cap] = true
	}
	for _, cap := range required {
		if !set[cap] {
			return false
		}
	}
	return true
}
