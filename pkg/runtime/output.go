package runtime

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/oslab/sysbox/pkg/config"
)

type OutputValue struct {
	Name        string    `json:"-"`
	Value       any       `json:"value"`
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Display     string    `json:"-"`
	Raw         cty.Value `json:"-"`
}

func EvaluateOutputs(root *config.Root, ctx *hcl.EvalContext) (map[string]OutputValue, error) {
	out := map[string]OutputValue{}
	if root == nil {
		return out, nil
	}
	for _, block := range root.Outputs {
		val := cty.NilVal
		display := "(unevaluated)"
		var native any
		typeName := "dynamic"
		if block.Value != nil {
			v, diags := block.Value.Value(ctx)
			if diags.HasErrors() {
				return nil, fmt.Errorf("output %s: %s", block.Name, diags.Error())
			}
			val = v
			display = OutputDisplayValue(v)
			native = OutputNativeValue(v)
			typeName = v.Type().FriendlyName()
		}
		out[block.Name] = OutputValue{
			Name:        block.Name,
			Value:       native,
			Type:        typeName,
			Description: block.Description,
			Display:     display,
			Raw:         val,
		}
	}
	return out, nil
}

// EvaluateOutputsBestEffort evaluates output blocks the same way EvaluateOutputs
// does, but tolerates individual failures: a block whose expression has
// diagnostics is skipped, and the remaining outputs are still returned. It is
// intended for read-side projections where one unresolvable reference (for
// example a state attribute not present in the synthetic eval context) must not
// blank the whole outputs map.
func EvaluateOutputsBestEffort(root *config.Root, ctx *hcl.EvalContext) map[string]OutputValue {
	out := map[string]OutputValue{}
	if root == nil {
		return out
	}
	for _, block := range root.Outputs {
		val := cty.NilVal
		display := "(unevaluated)"
		var native any
		typeName := "dynamic"
		if block.Value != nil {
			v, diags := block.Value.Value(ctx)
			if diags.HasErrors() {
				continue
			}
			val = v
			display = OutputDisplayValue(v)
			native = OutputNativeValue(v)
			typeName = v.Type().FriendlyName()
		}
		out[block.Name] = OutputValue{
			Name:        block.Name,
			Value:       native,
			Type:        typeName,
			Description: block.Description,
			Display:     display,
			Raw:         val,
		}
	}
	return out
}

func SortedOutputNames(outputs map[string]OutputValue) []string {
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func OutputDisplayValue(v cty.Value) string {
	if !v.IsKnown() {
		return "(unknown)"
	}
	if v.IsNull() {
		return "null"
	}
	if v.Type() == cty.String {
		return v.AsString()
	}
	data, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return v.GoString()
	}
	return string(data)
}

func OutputNativeValue(v cty.Value) any {
	if !v.IsKnown() || v.IsNull() {
		return nil
	}
	if v.Type() == cty.String {
		return v.AsString()
	}
	if v.Type() == cty.Bool {
		return v.True()
	}
	if v.Type() == cty.Number {
		f, _ := v.AsBigFloat().Float64()
		return f
	}
	data, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return v.GoString()
	}
	var native any
	if err := json.Unmarshal(data, &native); err != nil {
		return string(data)
	}
	return native
}
