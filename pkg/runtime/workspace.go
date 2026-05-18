package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// LoadWorkspace parses hclFile and loads stateFile, returning all objects
// needed to run plan/apply/destroy. It is the canonical entry point for both
// the CLI commands package and the HTTP API.
func LoadWorkspace(hclFile, stateFile string) (
	*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error,
) {
	root, err := config.ParseFile(hclFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("parse config: %w", err)
	}
	ctx := config.BuildEvalContext(root, filepath.Dir(hclFile))
	g, err := BuildGraph(root, ctx, hclFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}
	mgr := state.NewManager(stateFile)
	s, err := mgr.Load()
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("load state: %w", err)
	}
	return g, mgr, s, root, ctx, nil
}

// BuildGraph builds a dependency graph from a parsed config root.
// hclFile is the source file path; it is used to resolve module source paths.
// Pass "" when the caller path is not known (module blocks are skipped).
func BuildGraph(root *config.Root, ctx *hcl.EvalContext, hclFile ...string) (*graph.Graph, error) {
	callerFile := ""
	if len(hclFile) > 0 {
		callerFile = hclFile[0]
	}
	g := graph.New()
	for i := range root.Resources {
		if err := expandResource(root.Resources[i], g, ctx); err != nil {
			return nil, err
		}
	}
	for i := range root.Modules {
		if err := expandModule(root.Modules[i], g, ctx, callerFile); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// expandResource handles a single resource block, expanding count or for_each if present.
func expandResource(r config.ResourceBlock, g *graph.Graph, ctx *hcl.EvalContext) error {
	synBody, isSyn := r.Remain.(*hclsyntax.Body)
	if !isSyn {
		return addResourceToGraph(r, r.Name, ctx, g)
	}

	// ── count expansion ────────────────────────────────────────────────────
	if countAttr, hasCount := synBody.Attributes["count"]; hasCount {
		val, diag := countAttr.Expr.Value(ctx)
		if diag.HasErrors() {
			return fmt.Errorf("resource %s.%s: count eval: %s", r.Type, r.Name, diag.Error())
		}
		n, acc := val.AsBigFloat().Int64()
		if acc != 0 || n < 0 {
			return fmt.Errorf("resource %s.%s: count must be a non-negative integer", r.Type, r.Name)
		}
		attrsWithout := make(hclsyntax.Attributes, len(synBody.Attributes)-1)
		for k, v := range synBody.Attributes {
			if k != "count" {
				attrsWithout[k] = v
			}
		}
		remainBody := &hclsyntax.Body{
			Attributes: attrsWithout,
			Blocks:     synBody.Blocks,
			SrcRange:   synBody.SrcRange,
			EndRange:   synBody.EndRange,
		}
		for i := 0; i < int(n); i++ {
			rCopy := config.ResourceBlock{
				Type:   r.Type,
				Name:   fmt.Sprintf("%s[%d]", r.Name, i),
				Remain: remainBody,
			}
			if err := addResourceToGraph(rCopy, rCopy.Name, config.CountEvalContext(ctx, i), g); err != nil {
				return fmt.Errorf("count[%d]: %w", i, err)
			}
		}
		return nil
	}

	// ── for_each expansion ─────────────────────────────────────────────────
	synAttr, hasForEach := synBody.Attributes["for_each"]
	if !hasForEach {
		return addResourceToGraph(r, r.Name, ctx, g)
	}

	val, diag := synAttr.Expr.Value(ctx)
	if diag.HasErrors() {
		return fmt.Errorf("resource %s.%s: for_each eval: %s", r.Type, r.Name, diag.Error())
	}

	isMap := val.Type().IsObjectType() || val.Type().IsMapType()
	isSet := val.Type().IsSetType()
	if !isMap && !isSet {
		return fmt.Errorf("resource %s.%s: for_each must be a map, object, or set of strings, got %s",
			r.Type, r.Name, val.Type().FriendlyName())
	}
	if isSet && val.Type().ElementType() != cty.String {
		return fmt.Errorf("resource %s.%s: for_each set must contain strings, got set(%s)",
			r.Type, r.Name, val.Type().ElementType().FriendlyName())
	}

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

	if isSet {
		// For sets each.key == each.value == element; instance name = element string.
		it := val.ElementIterator()
		for it.Next() {
			_, elemVal := it.Element()
			key := elemVal.AsString()
			rCopy := config.ResourceBlock{
				Type:   r.Type,
				Name:   r.Name + "_" + key,
				Remain: remainBody,
			}
			if err := addResourceToGraph(rCopy, rCopy.Name, config.EachEvalContext(ctx, key, elemVal), g); err != nil {
				return fmt.Errorf("for_each[%s]: %w", key, err)
			}
		}
		return nil
	}

	for key, elemVal := range val.AsValueMap() {
		rCopy := config.ResourceBlock{
			Type:   r.Type,
			Name:   r.Name + "_" + key,
			Remain: remainBody,
		}
		if err := addResourceToGraph(rCopy, rCopy.Name, config.EachEvalContext(ctx, key, elemVal), g); err != nil {
			return fmt.Errorf("for_each[%s]: %w", key, err)
		}
	}
	return nil
}

// expandModule loads a module file, builds a variable eval context, and
// expands all its resources into the main graph with the prefix
// "module_<name>_". Module nesting is not supported (module-in-module errors).
func expandModule(mod config.ModuleBlock, g *graph.Graph, parentCtx *hcl.EvalContext, callerFile string) error {
	if callerFile == "" {
		return fmt.Errorf("module %q: cannot resolve source without a caller file path", mod.Name)
	}
	src, err := config.ResolveModuleSource(mod.Source, filepath.Dir(callerFile))
	if err != nil {
		return fmt.Errorf("module %q: %w", mod.Name, err)
	}

	modRoot, err := config.ParseFile(src)
	if err != nil {
		return fmt.Errorf("module %q: parse %s: %w", mod.Name, src, err)
	}
	if len(modRoot.Modules) > 0 {
		return fmt.Errorf("module %q: nested modules are not supported", mod.Name)
	}

	modCtx := config.ModuleEvalContext(mod, modRoot, mod.Name, parentCtx)
	prefix := "module_" + mod.Name + "_"

	for i := range modRoot.Resources {
		r := modRoot.Resources[i]
		// Expand for_each/count inside the module, then prefix all resulting names.
		sub := graph.New()
		if err := expandResource(r, sub, modCtx); err != nil {
			return fmt.Errorf("module %q resource %s.%s: %w", mod.Name, r.Type, r.Name, err)
		}
		for _, node := range sub.All() {
			// Re-prefix the name and rewrite deps that point to other module resources.
			nsName := prefix + node.ID.Name
			var nsDeps []graph.Ref
			for _, dep := range node.Deps {
				nsDeps = append(nsDeps, graph.Ref{Type: dep.Type, Name: prefix + dep.Name})
			}
			g.AddNode(node.ID.Type, nsName, nsDeps)
			g.SetData(node.ID.Type, nsName, node.Data)
		}
	}
	return nil
}

// addResourceToGraph decodes one resource block and adds it (with deps) to g.
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
			if parts := strings.SplitN(dep, ".", 2); len(parts) == 2 {
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
			if parts := strings.SplitN(dep, ".", 2); len(parts) == 2 {
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
			if parts := strings.SplitN(dep, ".", 2); len(parts) == 2 {
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

// decodeNodeProviderConfig resolves cfg.Substrate, validates the optional
// provider block label, and fills cfg.ProviderConfig.
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
