package commands

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
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

	case "sysbox_node":
		cfg := &config.NodeConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := resolveRef(cfg.Image); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
		}
		for _, link := range cfg.Links {
			if ref := resolveRef(link.Network); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
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
		if ref := resolveRef(cfg.Image); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
		}
		for _, iface := range cfg.Interfaces {
			if ref := resolveRef(iface.Network); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
			}
		}

	case "sysbox_firewall":
		cfg := &config.FirewallConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := resolveRef(cfg.AttachTo); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
		}

	case "sysbox_ssh_access":
		cfg := &config.SSHAccessConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := resolveRef(cfg.Node); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
		}

	case "sysbox_agent":
		cfg := &config.AgentConfig{}
		if err := config.DecodeResource(&r, cfg, ctx); err != nil {
			return err
		}
		data = cfg
		if ref := resolveRef(cfg.Node); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
		}
		for _, dep := range cfg.DependsOn {
			parts := strings.SplitN(dep, ".", 2)
			if len(parts) == 2 {
				deps = append(deps, graph.Ref{Type: parts[0], Name: parts[1]})
			}
		}

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
			if ref := resolveRef(cfg.Node); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_node", Name: ref})
			}
		} else {
			// external: depends on image + networks
			if ref := resolveRef(cfg.Image); ref != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
			}
			for _, link := range cfg.Links {
				if ref := resolveRef(link.Network); ref != "" {
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
			if ref := resolveRef(nodeRef); ref != "" {
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
		fmt.Printf("warning: unsupported resource type %q (skipped)\n", r.Type)
		return nil
	}

	g.AddNode(r.Type, name, deps)
	g.SetData(r.Type, name, data)
	return nil
}

// resolveRef accepts either a bare resource name (post-EvalContext) or a
// legacy quoted "type.name.id" reference and returns the resource name.
// Empty string means "could not resolve"; the caller should treat that as
// no dependency rather than an error so optional fields stay optional.
func resolveRef(ref string) string {
	if ref == "" {
		return ""
	}
	if !strings.Contains(ref, ".") {
		return ref
	}
	parts := strings.Split(ref, ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}
