package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// BuildEvalContext returns an *hcl.EvalContext that resolves bare-identifier
// references in resource bodies into the original block names.
//
// Two namespaces are exposed:
//
//   - substrate.<type>.<alias>      -> cty.StringVal("<type>")
//   - <resource_type>.<name>        -> cty.ObjectVal({"id": "<name>", "name": "<name>"})
//
// At parse time we don't know runtime IDs yet (e.g. docker container IDs);
// the executor uses the resource name as a stable lookup key into state.
// Phase 2 may extend this with apply-time post-processing.
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

	vars := map[string]cty.Value{}
	if len(substrateVal) > 0 {
		vars["substrate"] = cty.ObjectVal(substrateVal)
	}
	for typ, byName := range resTypes {
		vars[typ] = cty.ObjectVal(byName)
	}

	return &hcl.EvalContext{Variables: vars}
}
