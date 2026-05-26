package api

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) dispatchRun(ctx context.Context, run *Run, required []string) error {
	agent, err := s.selectAgent(ctx, required, run.AgentID)
	if err != nil {
		s.jobs.finish(run, err)
		return err
	}
	s.jobs.assign(run, agent.ID)
	if _, err := s.publishAgentCommand(ctx, agent.ID, controlplane.AgentCommand{
		Type: "run_assigned",
		Run:  ptrRun(runRecord(*run)),
	}); err != nil {
		return err
	}
	return nil
}

func ptrRun(run controlplane.Run) *controlplane.Run { return &run }

func (s *Server) selectAgent(ctx context.Context, required []string, preferred string) (controlplane.Agent, error) {
	if s.agents == nil {
		s.agents = newAgentRegistry()
	}
	agents := s.listAgents(ctx)
	required = normalizeCapabilities(required)
	if preferred != "" && preferred != DefaultAgentID {
		for _, agent := range agents {
			if agent.ID == preferred {
				if agent.Status != "online" || agent.Disabled || agent.Quarantined {
					return controlplane.Agent{}, fmt.Errorf("agent %q is not online", preferred)
				}
				if !hasCapabilities(agent.Capabilities, required) {
					return controlplane.Agent{}, fmt.Errorf("agent %q does not satisfy capabilities: required %v, has %v", preferred, required, normalizeCapabilities(agent.Capabilities))
				}
				return agent, nil
			}
		}
		return controlplane.Agent{}, fmt.Errorf("agent %q not found", preferred)
	}
	for _, agent := range agents {
		if agent.ID == DefaultAgentID {
			continue
		}
		if agent.Status != "online" || agent.Disabled || agent.Quarantined {
			continue
		}
		if hasCapabilities(agent.Capabilities, required) {
			return agent, nil
		}
	}
	return controlplane.Agent{}, fmt.Errorf("no online agent satisfies capabilities: %v", required)
}

func requiredCapabilitiesForTopology(path string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx := config.BuildEvalContext(root)
	set := map[string]bool{}
	for _, r := range root.Resources {
		cfg, err := decodeCapabilityResource(r, evalCtx)
		if err != nil {
			return nil, err
		}
		switch cfg := cfg.(type) {
		case *config.NodeConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		case *config.RouterConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		case *config.ImageConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		case *config.KernelConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		case *config.NetworkConfig:
			if !cfg.NAT {
				set["network"] = true
			}
		case *config.FirewallConfig:
			set["network"] = true
		case *config.SSHAccessConfig:
			set["network"] = true
		case *config.ActorConfig:
			set["docker"] = true
			if cfg.Position != "external" {
				set["network"] = true
			}
		}
	}
	return capabilitiesFromSet(set), nil
}

func requiredCapabilitiesForNode(path, node string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx := config.BuildEvalContext(root)
	set := map[string]bool{}
	for _, r := range root.Resources {
		if r.Name != node || (r.Type != "sysbox_node" && r.Type != "sysbox_router") {
			continue
		}
		cfg, err := decodeCapabilityResource(r, evalCtx)
		if err != nil {
			return nil, err
		}
		switch cfg := cfg.(type) {
		case *config.NodeConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		case *config.RouterConfig:
			addSubstrateCapabilities(set, cfg.Substrate)
		}
		return capabilitiesFromSet(set), nil
	}
	return nil, fmt.Errorf("node %q not found in topology", node)
}

func decodeCapabilityResource(r config.ResourceBlock, evalCtx *hcl.EvalContext) (any, error) {
	switch r.Type {
	case "sysbox_node":
		cfg := &config.NodeConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_router":
		cfg := &config.RouterConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_image":
		cfg := &config.ImageConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_kernel":
		cfg := &config.KernelConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_network":
		cfg := &config.NetworkConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_firewall":
		cfg := &config.FirewallConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_ssh_access":
		cfg := &config.SSHAccessConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	case "sysbox_actor":
		cfg := &config.ActorConfig{}
		if err := config.DecodeResource(&r, cfg, evalCtx); err != nil {
			return nil, err
		}
		return cfg, nil
	default:
		return nil, nil
	}
}

func addSubstrateCapabilities(set map[string]bool, substrate string) {
	switch substrate {
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
		set[substrate] = true
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
