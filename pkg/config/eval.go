package config

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// BuildEvalContext returns an *hcl.EvalContext that resolves bare-identifier
// references in resource bodies into the original block names.
//
// Namespaces exposed:
//
//   - substrate.<type>.<alias>      -> cty.StringVal("<type>")
//   - <resource_type>.<name>        -> cty.ObjectVal({"id": "<name>", "name": "<name>"})
//   - local.<key>                   -> cty.StringVal("<value>")  (from locals blocks)
//
// At parse time we don't know runtime IDs yet (e.g. docker container IDs);
// the executor uses the resource name as a stable lookup key into state.
func BuildEvalContext(root *Root) *hcl.EvalContext {
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

	return &hcl.EvalContext{
		Variables: vars,
		Functions: map[string]function.Function{
			"env":   envFunc,
			"toset": tosetFunc,
		},
	}
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

// ParseLocals extracts the resolved local values from the root for use
// by the apply command (e.g. output printing). Returns an empty map if none.
func ParseLocals(root *Root, ctx *hcl.EvalContext) map[string]string {
	out := map[string]string{}
	for _, lb := range root.Locals {
		if lb.Remain == nil {
			continue
		}
		attrs, diags := lb.Remain.JustAttributes()
		if diags.HasErrors() {
			continue
		}
		for name, attr := range attrs {
			val, diags := attr.Expr.Value(ctx)
			if diags.HasErrors() {
				continue
			}
			if val.Type() == cty.String {
				out[name] = val.AsString()
			}
		}
	}
	return out
}
