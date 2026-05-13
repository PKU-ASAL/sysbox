package config

import (
	"os"

	"github.com/hashicorp/hcl/v2"
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

	resTypes := map[string]map[string]cty.Value{}
	for _, r := range root.Resources {
		if resTypes[r.Type] == nil {
			resTypes[r.Type] = map[string]cty.Value{}
		}
		resTypes[r.Type][r.Name] = cty.ObjectVal(map[string]cty.Value{
			"id":   cty.StringVal(r.Name),
			"name": cty.StringVal(r.Name),
		})
	}

	// Collect locals: merge all locals blocks into one map.
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
			val, diags := attr.Expr.Value(nil)
			if diags.HasErrors() {
				continue
			}
			localVals[name] = val
		}
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
			"env": envFunc,
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
