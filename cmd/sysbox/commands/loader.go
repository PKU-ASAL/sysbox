package commands

import (
	"fmt"
	"strings"

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

	g := graph.New()
	if err := buildGraph(root, g); err != nil {
		return nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load state: %w", err)
	}
	return g, mgr, s, nil
}

func buildGraph(root *config.Root, g *graph.Graph) error {
	for i := range root.Resources {
		r := root.Resources[i]
		var deps []graph.Ref
		var data any

		switch r.Type {
		case "sysbox_network":
			cfg := &config.NetworkConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg

		case "sysbox_image":
			cfg := &config.ImageConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg

		case "sysbox_node":
			cfg := &config.NodeConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg
			if imgName, err := resolveImageRef(cfg.Image); err == nil {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: imgName})
			}
			for _, link := range cfg.Links {
				if netName, err := resolveNetworkRef(link.Network); err == nil {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: netName})
				}
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

func resolveImageRef(ref string) (string, error) {
	parts := strings.Split(ref, ".")
	if len(parts) >= 2 {
		return parts[1], nil
	}
	return "", fmt.Errorf("bad image ref: %s", ref)
}

func resolveNetworkRef(ref string) (string, error) {
	parts := strings.Split(ref, ".")
	if len(parts) >= 2 {
		return parts[1], nil
	}
	return "", fmt.Errorf("bad network ref: %s", ref)
}
