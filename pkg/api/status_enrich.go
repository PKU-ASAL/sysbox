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
// a JSON string for any type it does not recognise rather than failing the
// whole enrichment.
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
		// Fall back: marshal to JSON and re-parse, or StringVal.
		if b, err := json.Marshal(t); err == nil {
			return cty.StringVal(string(b))
		}
		return cty.StringVal(fmt.Sprintf("%v", t))
	}
}

// enrichEvalContext overrides the synthetic resource type→name bindings in the
// eval context with the resource's real state attributes, so output
// expressions such as sysbox_node.web.primary_ip resolve. substrate, local and
// module namespaces are left intact.
//
// Only non-count, non-for_each resources (a single instance per HCL block
// name) are enriched: those have an unset Address.Key. Count/for_each
// instances keep their synthetic {id, name} binding and are a separate
// follow-up.
func enrichEvalContext(ctx *hcl.EvalContext, st *state.State) {
	if ctx == nil || st == nil {
		return
	}

	byType := map[string]map[string]cty.Value{}
	for _, r := range st.Resources {
		if r.Address.Key.IsSet() {
			continue // count/for_each: leave as synthetic, follow-up
		}
		if byType[r.Address.Type] == nil {
			byType[r.Address.Type] = map[string]cty.Value{}
		}
		attrs := map[string]cty.Value{
			"id":   cty.StringVal(r.Address.String()),
			"name": cty.StringVal(r.Address.String()),
		}
		for k, v := range r.Attributes {
			attrs[k] = goToCty(v)
		}
		// Address-derived id/name always win over any colliding attribute key.
		attrs["id"] = cty.StringVal(r.Address.String())
		attrs["name"] = cty.StringVal(r.Address.String())
		byType[r.Address.Type][r.Address.Name] = cty.ObjectVal(attrs)
	}

	for typ, byName := range byType {
		ctx.Variables[typ] = cty.ObjectVal(byName)
	}
}
