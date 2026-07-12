package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// LoadWorkspace parses hclFile and loads stateFile, returning all objects
// needed to run plan/apply/destroy. It is the canonical entry point for both
// the CLI commands package and the HTTP API.
func LoadWorkspace(hclFile, stateFile string) (
	*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error,
) {
	return LoadWorkspaceWithManager(hclFile, state.NewManager(stateFile))
}

func LoadWorkspaceWithManager(hclFile string, mgr *state.Manager) (
	*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error,
) {
	root, err := config.ParseFile(hclFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("parse config: %w", err)
	}
	ctx, err := config.BuildEvalContext(root, filepath.Dir(hclFile))
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("evaluate config: %w", err)
	}
	g, err := BuildGraph(root, ctx, hclFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}
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
	// Data blocks: add them to the graph as read-only nodes. They are
	// executed during apply but never create/destroy infrastructure.
	for i := range root.Data {
		if err := expandDataBlock(root.Data[i], g, ctx); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// expandResource handles a single resource block, expanding count or for_each if present.
func expandResource(r config.ResourceBlock, g *graph.Graph, ctx *hcl.EvalContext) error {
	return expandResourceAt(r, g, ctx, nil)
}

func expandResourceAt(r config.ResourceBlock, g *graph.Graph, ctx *hcl.EvalContext, modules []address.ModuleInstance) error {
	synBody, isSyn := r.Remain.(*hclsyntax.Body)
	if !isSyn {
		return addResourceToGraph(r, withModulePath(address.Resource(r.Type, r.Name), modules), ctx, g)
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
			rCopy := config.ResourceBlock{Type: r.Type, Name: r.Name, Remain: remainBody}
			addr := withModulePath(address.IntInstance(r.Type, r.Name, i), modules)
			if err := addResourceToGraph(rCopy, addr, config.CountEvalContext(ctx, i), g); err != nil {
				return fmt.Errorf("count[%d]: %w", i, err)
			}
		}
		return nil
	}

	// ── for_each expansion ─────────────────────────────────────────────────
	synAttr, hasForEach := synBody.Attributes["for_each"]
	if !hasForEach {
		return addResourceToGraph(r, withModulePath(address.Resource(r.Type, r.Name), modules), ctx, g)
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
			rCopy := config.ResourceBlock{Type: r.Type, Name: r.Name, Remain: remainBody}
			addr := withModulePath(address.StringInstance(r.Type, r.Name, key), modules)
			if err := addResourceToGraph(rCopy, addr, config.EachEvalContext(ctx, key, elemVal), g); err != nil {
				return fmt.Errorf("for_each[%s]: %w", key, err)
			}
		}
		return nil
	}

	for key, elemVal := range val.AsValueMap() {
		rCopy := config.ResourceBlock{Type: r.Type, Name: r.Name, Remain: remainBody}
		addr := withModulePath(address.StringInstance(r.Type, r.Name, key), modules)
		if err := addResourceToGraph(rCopy, addr, config.EachEvalContext(ctx, key, elemVal), g); err != nil {
			return fmt.Errorf("for_each[%s]: %w", key, err)
		}
	}
	return nil
}

// expandModule loads a module file and expands its resources with a structured
// module path. Module nesting is not supported.
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

	modCtx, err := config.ModuleEvalContext(mod, modRoot, mod.Name, parentCtx)
	if err != nil {
		return fmt.Errorf("module %q variables: %w", mod.Name, err)
	}
	modulePath := []address.ModuleInstance{{Name: mod.Name}}

	for i := range modRoot.Resources {
		r := modRoot.Resources[i]
		if err := expandResourceAt(r, g, modCtx, modulePath); err != nil {
			return fmt.Errorf("module %q resource %s.%s: %w", mod.Name, r.Type, r.Name, err)
		}
	}
	return nil
}

// addResourceToGraph decodes one resource block and adds it (with deps) to g.
func addResourceToGraph(r config.ResourceBlock, addr address.Address, ctx *hcl.EvalContext, g *graph.Graph) error {
	provider, ok := GetResourceProvider(r.Type)
	if !ok {
		fmt.Fprintf(os.Stderr, "warning: unsupported resource type %q (skipped)\n", r.Type)
		return nil
	}
	decoder, ok := provider.(ResourceGraphDecoder)
	if !ok {
		return fmt.Errorf("resource type %q does not support graph decoding", r.Type)
	}
	data, deps, err := decoder.DecodeResource(r, addr.Name, ctx)
	if err != nil {
		return err
	}

	if len(addr.ModulePath) > 0 {
		for i := range deps {
			if len(deps[i].ModulePath) == 0 && deps[i].Type != "substrate" {
				deps[i] = withModulePath(deps[i], addr.ModulePath)
			}
		}
	}
	if err := g.AddNode(addr, deps); err != nil {
		return err
	}
	if err := g.SetData(addr, data); err != nil {
		return err
	}
	return nil
}

func withModulePath(addr address.Address, modules []address.ModuleInstance) address.Address {
	for _, module := range modules {
		addr = addr.WithModule(module)
	}
	return addr
}

// expandDataBlock decodes a data block and adds it to the graph as a
// read-only node. Data nodes carry their decoded config in Data so that
// the executor can call substrate.ReadNode during apply.
func expandDataBlock(d config.DataBlock, g *graph.Graph, ctx *hcl.EvalContext) error {
	typ := "data_" + d.Type
	provider, ok := GetResourceProvider(typ)
	if !ok {
		return fmt.Errorf("data block type %q not supported; supported: sysbox_node, sysbox_network, sysbox_image", d.Type)
	}
	decoder, ok := provider.(DataGraphDecoder)
	if !ok {
		return fmt.Errorf("data block type %q does not support graph decoding", d.Type)
	}
	data, deps, err := decoder.DecodeData(d, ctx)
	if err != nil {
		return err
	}

	// Data blocks use a "data_" prefix in the graph to distinguish from resources.
	addr := address.Resource(typ, d.Name)
	if err := g.AddNode(addr, deps); err != nil {
		return err
	}
	return g.SetData(addr, data)
}
