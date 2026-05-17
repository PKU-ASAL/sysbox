package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// loadWorkspaceWithRoot is like loadWorkspace but also returns the parsed Root
// for output/locals access.
func loadWorkspaceWithRoot() (*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error) {
	root, err := config.ParseFile(flagConfigFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("parse config: %w", err)
	}

	ctx := config.BuildEvalContext(root)

	g := graph.New()
	if err := buildGraph(root, g, ctx); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("load state: %w", err)
	}
	return g, mgr, s, root, ctx, nil
}

// loadWorkspace parses the HCL file into a graph and loads the state.
func loadWorkspace() (*graph.Graph, *state.Manager, *state.State, error) {
	root, err := config.ParseFile(flagConfigFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse config: %w", err)
	}

	ctx := config.BuildEvalContext(root)

	g := graph.New()
	if err := buildGraph(root, g, ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load state: %w", err)
	}
	return g, mgr, s, nil
}

// decodeNodeProviderConfig resolves cfg.Substrate, validates the optional
// `provider "X" {}` label matches, and fills cfg.ProviderConfig with the
// substrate-owned typed value (via Substrate.DecodeProviderConfig).
func decodeNodeProviderConfig(cfg *config.NodeConfig, ctx *hcl.EvalContext) error {
	subName, err := config.ResolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	var body hcl.Body
	switch len(cfg.Providers) {
	case 0:
		body = nil
	case 1:
		pb := cfg.Providers[0]
		if pb.Type != subName {
			return fmt.Errorf("provider %q block does not match substrate %q", pb.Type, subName)
		}
		body = pb.Remain
	default:
		return fmt.Errorf("at most one provider block allowed per node, got %d", len(cfg.Providers))
	}
	pc, err := sub.DecodeProviderConfig(body, ctx)
	if err != nil {
		return err
	}
	cfg.ProviderConfig = pc
	return nil
}

func buildGraph(root *config.Root, g *graph.Graph, ctx *hcl.EvalContext) error {
	for i := range root.Resources {
		r := root.Resources[i]
		if err := expandResource(r, g, ctx); err != nil {
			return err
		}
	}
	return nil
}

// expandResource handles a single resource block. If it has a for_each
// meta-argument (detected via hclsyntax type assertion), it expands into one
// graph node per map key. Falls back to single-resource for non-hclsyntax bodies
// or when for_each is absent.
func expandResource(r config.ResourceBlock, g *graph.Graph, ctx *hcl.EvalContext) error {
	synBody, isSyn := r.Remain.(*hclsyntax.Body)
	if !isSyn {
		return addResourceToGraph(r, r.Name, ctx, g)
	}

	synAttr, hasForEach := synBody.Attributes["for_each"]
	if !hasForEach {
		return addResourceToGraph(r, r.Name, ctx, g)
	}

	// Evaluate the for_each expression.
	val, diag := synAttr.Expr.Value(ctx)
	if diag.HasErrors() {
		return fmt.Errorf("resource %s.%s: for_each eval: %s", r.Type, r.Name, diag.Error())
	}
	if !val.Type().IsObjectType() && !val.Type().IsMapType() {
		return fmt.Errorf("resource %s.%s: for_each must be an object or map, got %s",
			r.Type, r.Name, val.Type().FriendlyName())
	}

	// Build a remain body without the for_each attribute for instance decodes.
	attrsWithout := make(hclsyntax.Attributes, len(synBody.Attributes)-1)
	for k, v := range synBody.Attributes {
		if k != "for_each" {
			attrsWithout[k] = v
		}
	}
	remainBody := &hclsyntax.Body{
		Attributes: attrsWithout,
		Blocks:     synBody.Blocks,
		SrcRange:   synBody.SrcRange,
		EndRange:   synBody.EndRange,
	}

	// Expand one instance per key.
	for key, elemVal := range val.AsValueMap() {
		instanceName := r.Name + "_" + key
		eachCtx := config.EachEvalContext(ctx, key, elemVal)
		rCopy := config.ResourceBlock{
			Type:   r.Type,
			Name:   instanceName,
			Remain: remainBody,
		}
		if err := addResourceToGraph(rCopy, instanceName, eachCtx, g); err != nil {
			return fmt.Errorf("for_each[%s]: %w", key, err)
		}
	}
	return nil
}

func addResourceToGraph(r config.ResourceBlock, name string, ctx *hcl.EvalContext, g *graph.Graph) error {
	var deps []graph.Ref
	var data any

	switch r.Type {
	case "sysbox_network":
		cfg := &config.NetworkConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg

	case "sysbox_image":
		cfg := &config.ImageConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg

	case "sysbox_kernel":
		cfg := &config.KernelConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		for _, dep := range cfg.DependsOn {
			parts := strings.SplitN(dep, ".", 2)
			if len(parts) == 2 {
				deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
			}
		}

	case "sysbox_node":
		cfg := &config.NodeConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		if err := decodeNodeProviderConfig(cfg, ctx); err != nil {
			return fmt.Errorf("resource sysbox_node.%s: %w", name, err)
		}
		data = cfg
		if ref := config.ResolveName(cfg.Image); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
		}
		for _, link := range cfg.Links {
			if ref := config.ResolveName(link.Network); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
			}
		}
		// Substrate-specific dependencies (e.g. firecracker -> sysbox_kernel)
		// surface through Substrate.Dependencies(cfg.ProviderConfig).
		if subName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
			if sub, err := substrate.Get(subName); err == nil {
				pd := sub.Dependencies(cfg.ProviderConfig)
				for _, n := range pd.Kernels {
					deps = append(deps, graph.Ref{Type: "sysbox_kernel", Name: n})
				}
				for _, n := range pd.Images {
					deps = append(deps, graph.Ref{Type: "sysbox_image", Name: n})
				}
				for _, n := range pd.Networks {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: n})
				}
			}
		}
		for _, dep := range cfg.DependsOn {
			parts := strings.SplitN(dep, ".", 2)
			if len(parts) == 2 {
				deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
			}
		}

	case "sysbox_router":
		cfg := &config.RouterConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := config.ResolveName(cfg.Image); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
		}
		for _, iface := range cfg.Interfaces {
			if ref := config.ResolveName(iface.Network); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
			}
		}

	case "sysbox_firewall":
		cfg := &config.FirewallConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := config.ResolveName(cfg.AttachTo); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
		}

	case "sysbox_ssh_access":
		cfg := &config.SSHAccessConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := config.ResolveName(cfg.Node); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
		}

	case "sysbox_agent":
		return fmt.Errorf("sysbox_agent is removed; use sysbox_actor with position = \"internal\" instead")

	case "sysbox_actor":
		cfg := &config.ActorConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		position := cfg.Position
		if position == "" {
			position = "internal"
		}
		if position == "internal" {
			if ref := config.ResolveName(cfg.Node); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
			}
		} else {
			// external: depends on image + networks
			if ref := config.ResolveName(cfg.Image); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
			}
			for _, link := range cfg.Links {
				if ref := config.ResolveName(link.Network); ref != "" {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
				}
			}
		}
		for _, dep := range cfg.DependsOn {
			parts := strings.SplitN(dep, ".", 2)
			if len(parts) == 2 {
				deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
			}
		}

	case "sysbox_monitor":
		cfg := &config.MonitorConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		for _, nodeRef := range cfg.Nodes {
			if ref := config.ResolveName(nodeRef); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
			}
		}
		for _, dep := range cfg.DependsOn {
			parts := strings.SplitN(dep, ".", 2)
			if len(parts) == 2 {
				deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
			}
		}

	default:
		fmt.Fprintf(os.Stderr, "warning: unsupported resource type %q (skipped)\n", r.Type)
		return nil
	}

	g.AddNode(r.Type, name, deps)
	g.SetData(r.Type, name, data)
	return nil
}
