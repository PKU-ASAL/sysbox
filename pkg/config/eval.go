package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// BuildEvalContext returns an *hcl.EvalContext for the given root. callerDir
// is the directory of the HCL file (needed to resolve module source paths).
// Pass "" when the caller directory is not known (module outputs won't be pre-loaded).
func BuildEvalContext(root *Root, callerDir ...string) *hcl.EvalContext {
	dir := ""
	if len(callerDir) > 0 {
		dir = callerDir[0]
	}
	return buildEvalContextInner(root, dir)
}

// buildEvalContextInner is the actual implementation.
func buildEvalContextInner(root *Root, callerDir string) *hcl.EvalContext {
	subTypes := map[string]map[string]cty.Value{}
	for _, sb := range root.Substrates {
		if subTypes[sb.Type] == nil {
			subTypes[sb.Type] = map[string]cty.Value{}
		}
		alias := sb.Alias
		if alias == "" {
			alias = "default"
		}
		subTypes[sb.Type][alias] = cty.StringVal(sb.Type)
	}

	substrateVal := map[string]cty.Value{}
	for typ, byAlias := range subTypes {
		substrateVal[typ] = cty.ObjectVal(byAlias)
	}

	// Collect locals first so they are available when evaluating count expressions.
	localCtx := &hcl.EvalContext{
		Functions: map[string]function.Function{"env": envFunc, "toset": tosetFunc},
	}
	localVals := map[string]cty.Value{}
	for _, lb := range root.Locals {
		if lb.Remain == nil {
			continue
		}
		attrs, diags := lb.Remain.JustAttributes()
		if diags.HasErrors() {
			continue
		}
		for name, attr := range attrs {
			val, diags := attr.Expr.Value(localCtx)
			if diags.HasErrors() {
				continue
			}
			localVals[name] = val
		}
	}

	// Minimal context for evaluating count = <expr> (literals + local.x).
	preCtx := &hcl.EvalContext{
		Variables: map[string]cty.Value{},
		Functions: map[string]function.Function{"env": envFunc, "toset": tosetFunc},
	}
	if len(localVals) > 0 {
		preCtx.Variables["local"] = cty.ObjectVal(localVals)
	}

	resTypes := map[string]map[string]cty.Value{}
	for _, r := range root.Resources {
		if resTypes[r.Type] == nil {
			resTypes[r.Type] = map[string]cty.Value{}
		}
		// Check for count: expose as a tuple so sysbox_node.name[i].id resolves.
		synBody, isSyn := r.Remain.(*hclsyntax.Body)
		if isSyn {
			if countAttr, hasCount := synBody.Attributes["count"]; hasCount {
				if val, diag := countAttr.Expr.Value(preCtx); !diag.HasErrors() {
					// Guard: count must be a number type. Non-number values
					// (strings, bools, lists) would panic on AsBigFloat().
					if val.Type() == cty.Number {
						if n, acc := val.AsBigFloat().Int64(); acc == 0 && n > 0 {
							elems := make([]cty.Value, n)
							for i := 0; i < int(n); i++ {
								instanceName := fmt.Sprintf("%s[%d]", r.Name, i)
								elems[i] = cty.ObjectVal(map[string]cty.Value{
									"id":   cty.StringVal(instanceName),
									"name": cty.StringVal(instanceName),
								})
							}
							resTypes[r.Type][r.Name] = cty.TupleVal(elems)
							continue
						}
					} // end if val.Type() == cty.Number
				}
			}
		}
		resTypes[r.Type][r.Name] = cty.ObjectVal(map[string]cty.Value{
			"id":   cty.StringVal(r.Name),
			"name": cty.StringVal(r.Name),
		})
	}

	vars := map[string]cty.Value{}
	if len(substrateVal) > 0 {
		vars["substrate"] = cty.ObjectVal(substrateVal)
	}
	for typ, byName := range resTypes {
		vars[typ] = cty.ObjectVal(byName)
	}
	if len(localVals) > 0 {
		vars["local"] = cty.ObjectVal(localVals)
	}

	ctx := &hcl.EvalContext{
		Variables: vars,
		Functions: map[string]function.Function{
			"env":   envFunc,
			"toset": tosetFunc,
		},
	}

	// Pre-load module outputs so that module.<name>.<key> can be referenced
	// in the caller. Each module resource is exposed with namespaced IDs so
	// that output expressions evaluate to the correct state lookup key.
	if callerDir != "" && len(root.Modules) > 0 {
		moduleVals := map[string]cty.Value{}
		for _, mod := range root.Modules {
			outVals := resolveModuleOutputs(mod, callerDir, ctx)
			if len(outVals) > 0 {
				moduleVals[mod.Name] = cty.ObjectVal(outVals)
			}
		}
		if len(moduleVals) > 0 {
			ctx.Variables["module"] = cty.ObjectVal(moduleVals)
		}
	}
	return ctx
}

// envFunc implements env("VAR_NAME") → string value from the host environment.
// Returns an empty string when the variable is not set (never errors).
var envFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "name", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		return cty.StringVal(os.Getenv(args[0].AsString())), nil
	},
})

// tosetFunc implements toset([...]) → set of the same element type.
// Mirrors Terraform's toset() conversion function.
var tosetFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "v", Type: cty.DynamicPseudoType, AllowDynamicType: true},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		arg := args[0]
		if !arg.Type().IsListType() && !arg.Type().IsTupleType() && !arg.Type().IsSetType() {
			return cty.NilType, fmt.Errorf("toset requires a list, tuple, or set")
		}
		// Determine element type from first non-null element.
		it := arg.ElementIterator()
		if !it.Next() {
			return cty.Set(cty.String), nil // empty → default string set
		}
		_, v := it.Element()
		return cty.Set(v.Type()), nil
	},
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		arg := args[0]
		if arg.LengthInt() == 0 {
			return cty.SetValEmpty(retType.ElementType()), nil
		}
		seen := map[string]cty.Value{}
		it := arg.ElementIterator()
		for it.Next() {
			_, v := it.Element()
			seen[v.GoString()] = v
		}
		elems := make([]cty.Value, 0, len(seen))
		for _, v := range seen {
			elems = append(elems, v)
		}
		return cty.SetVal(elems), nil
	},
})

// resolveModuleOutputs loads a module file, builds a namespaced eval context
// for it, evaluates its output blocks, and returns the output values keyed by
// output name. Errors are silently ignored (best-effort; parse-time only).
func resolveModuleOutputs(mod ModuleBlock, callerDir string, parentCtx *hcl.EvalContext) map[string]cty.Value {
	src, err := resolveModuleSource(mod.Source, callerDir)
	if err != nil {
		return nil
	}
	modRoot, err := ParseFile(src)
	if err != nil {
		return nil
	}
	// Build the module's eval context with namespaced resource IDs and
	// variables from the module block's attributes.
	modCtx := buildModuleEvalContext(mod, modRoot, mod.Name, parentCtx)

	outVals := map[string]cty.Value{}
	for _, out := range modRoot.Outputs {
		if out.Value == nil {
			continue
		}
		val, diag := out.Value.Value(modCtx)
		if diag.HasErrors() {
			continue
		}
		outVals[out.Name] = val
	}
	return outVals
}

// buildModuleEvalContext builds an eval context for a module's resources with
// namespaced IDs (module_<modName>_<resourceName>) and var.xxx bindings.
func buildModuleEvalContext(mod ModuleBlock, modRoot *Root, modName string, parentCtx *hcl.EvalContext) *hcl.EvalContext {
	// Compute variable defaults from variable blocks.
	varDefaults := map[string]cty.Value{}
	for _, vb := range modRoot.Variables {
		if vb.Remain == nil {
			continue
		}
		attrs, diag := vb.Remain.JustAttributes()
		if diag.HasErrors() {
			continue
		}
		if defAttr, ok := attrs["default"]; ok {
			if val, diag := defAttr.Expr.Value(nil); !diag.HasErrors() {
				varDefaults[vb.Name] = val
			}
		}
	}

	// Evaluate variable assignments from the module block's Remain body.
	varVals := make(map[string]cty.Value, len(varDefaults))
	for k, v := range varDefaults {
		varVals[k] = v
	}
	if mod.Remain != nil {
		if attrs, diag := mod.Remain.JustAttributes(); !diag.HasErrors() {
			for k, attr := range attrs {
				if val, diag := attr.Expr.Value(parentCtx); !diag.HasErrors() {
					varVals[k] = val
				}
			}
		}
	}

	// Build namespaced resource type variables: id = "module_<modName>_<name>".
	prefix := "module_" + modName + "_"
	resTypes := map[string]map[string]cty.Value{}
	for _, r := range modRoot.Resources {
		if resTypes[r.Type] == nil {
			resTypes[r.Type] = map[string]cty.Value{}
		}
		nsName := prefix + r.Name
		resTypes[r.Type][r.Name] = cty.ObjectVal(map[string]cty.Value{
			"id":   cty.StringVal(nsName),
			"name": cty.StringVal(nsName),
		})
	}

	vars := map[string]cty.Value{}
	for typ, byName := range resTypes {
		vars[typ] = cty.ObjectVal(byName)
	}
	if len(varVals) > 0 {
		vars["var"] = cty.ObjectVal(varVals)
	}

	child := parentCtx.NewChild()
	child.Variables = vars
	return child
}

// ModuleEvalContext is the public helper used by workspace.go to build the
// eval context for expanding a module's resources into the main graph.
func ModuleEvalContext(mod ModuleBlock, modRoot *Root, modName string, parentCtx *hcl.EvalContext) *hcl.EvalContext {
	return buildModuleEvalContext(mod, modRoot, modName, parentCtx)
}

// ResolveModuleSource converts a module source path to an absolute file path.
// If source points to a directory, looks for the first *.sysbox.hcl file inside.
func ResolveModuleSource(source, callerDir string) (string, error) {
	return resolveModuleSource(source, callerDir)
}

func resolveModuleSource(source, callerDir string) (string, error) {
	path := source
	if !filepath.IsAbs(path) {
		path = filepath.Join(callerDir, path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("module source %q: %w", source, err)
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return "", fmt.Errorf("module source dir %q: %w", source, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sysbox.hcl") {
				return filepath.Join(path, e.Name()), nil
			}
		}
		// Fallback: main.hcl
		main := filepath.Join(path, "main.hcl")
		if _, err := os.Stat(main); err == nil {
			return main, nil
		}
		return "", fmt.Errorf("module source dir %q: no *.sysbox.hcl or main.hcl found", source)
	}
	return path, nil
}

// CountEvalContext returns a child EvalContext that exposes count.index
// for use inside a count-expanded resource body.
func CountEvalContext(parent *hcl.EvalContext, index int) *hcl.EvalContext {
	child := parent.NewChild()
	child.Variables = map[string]cty.Value{
		"count": cty.ObjectVal(map[string]cty.Value{
			"index": cty.NumberIntVal(int64(index)),
		}),
	}
	return child
}

// EachEvalContext returns a child EvalContext that exposes each.key and
// each.value for use inside a for_each resource body.
func EachEvalContext(parent *hcl.EvalContext, key string, value cty.Value) *hcl.EvalContext {
	// hcl.EvalContext.Parent is unexported; use NewChild instead.
	child := parent.NewChild()
	child.Variables = map[string]cty.Value{
		"each": cty.ObjectVal(map[string]cty.Value{
			"key":   cty.StringVal(key),
			"value": value,
		}),
	}
	return child
}
