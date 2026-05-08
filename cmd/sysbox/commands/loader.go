package commands

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

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
			if name := resolveRef(cfg.Image); name != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: name})
			}
			for _, link := range cfg.Links {
				if name := resolveRef(link.Network); name != "" {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: name})
				}
			}

		case "sysbox_router":
			cfg := &config.RouterConfig{}
			if err := config.DecodeResource(&r, cfg, ctx); err != nil {
				return err
			}
			data = cfg
			if name := resolveRef(cfg.Image); name != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: name})
			}
			for _, iface := range cfg.Interfaces {
				if name := resolveRef(iface.Network); name != "" {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: name})
				}
			}

		case "sysbox_firewall":
			cfg := &config.FirewallConfig{}
			if err := config.DecodeResource(&r, cfg, ctx); err != nil {
				return err
			}
			data = cfg
			if name := resolveRef(cfg.AttachTo); name != "" {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: name})
			}

		default:
			fmt.Printf("warning: unsupported resource type %q (skipped)\n", r.Type)
			continue
		}

		g.AddNode(r.Type, r.Name, deps)
		g.SetData(r.Type, r.Name, data)
	}
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
