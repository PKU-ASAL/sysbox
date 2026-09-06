package api

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/state"
)

// goToCty converts a Go value read from state (JSON round-tripped) into a
// cty.Value so it can be bound into an eval context. Attribute values are
// typically scalars, maps, or slices, but the converter degrades gracefully to
// a JSON round-trip (and finally a string) for any type it does not recognise
// rather than failing the whole enrichment.
func goToCty(v any) cty.Value {
	switch t := v.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType)
	case string:
		return cty.StringVal(t)
	case bool:
		return cty.BoolVal(t)
	case int:
		return cty.NumberIntVal(int64(t))
	case int64:
		return cty.NumberIntVal(t)
	case float64:
		return cty.NumberFloatVal(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return cty.NumberFloatVal(f)
		}
		return cty.StringVal(t.String())
	case map[string]any:
		if len(t) == 0 {
			return cty.EmptyObjectVal
		}
		obj := make(map[string]cty.Value, len(t))
		for k, v := range t {
			obj[k] = goToCty(v)
		}
		return cty.ObjectVal(obj)
	case []any:
		if len(t) == 0 {
			return cty.EmptyTupleVal
		}
		items := make([]cty.Value, len(t))
		for i, v := range t {
			items[i] = goToCty(v)
		}
		return cty.TupleVal(items)
	default:
		// Best-effort: marshal to JSON and re-parse so composite values reach
		// the map/slice branches above; a flat string is the final fallback.
		b, err := json.Marshal(t)
		if err != nil {
			return cty.StringVal(fmt.Sprintf("%v", t))
		}
		var parsed any
		if err := json.Unmarshal(b, &parsed); err != nil {
			return cty.StringVal(string(b))
		}
		return goToCty(parsed)
	}
}

// enrichEvalContext merges each non-count root resource's real state attributes
// into the eval context, so output expressions such as
// sysbox_node.web.primary_ip resolve. The merge preserves every binding
// BuildEvalContext already placed in the type namespace — count/for_each tuples
// and HCL-declared resources absent from state — and skips module resources
// (Address.ModulePath set) so they cannot clobber root resources of the same
// type/name. substrate, local and module namespaces are left intact.
func enrichEvalContext(ctx *hcl.EvalContext, st *state.State) {
	if ctx == nil || st == nil {
		return
	}

	for _, r := range st.Resources {
		if r.Address.Key.IsSet() || len(r.Address.ModulePath) > 0 {
			continue // count/for_each and module resources: leave intact
		}
		typVal, ok := ctx.Variables[r.Address.Type]
		if !ok || !typVal.Type().IsObjectType() {
			continue
		}

		attrs := map[string]cty.Value{}
		for k, v := range r.Attributes {
			attrs[k] = goToCty(v)
		}
		// id/name always win over any colliding attribute key.
		attrs["id"] = cty.StringVal(r.Address.String())
		attrs["name"] = cty.StringVal(r.Address.String())

		merged := typVal.AsValueMap()
		merged[r.Address.Name] = cty.ObjectVal(attrs)
		ctx.Variables[r.Address.Type] = cty.ObjectVal(merged)
	}
}
