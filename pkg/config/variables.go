package config

import (
	"fmt"

	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/secret"
)

// VariableBindings computes the var.<name> → value bindings for the eval
// context from a set of variable blocks and the apply-time inputs.
//
// A sensitive variable is bound to its secret://input/<name> reference, never
// to the plaintext input value — that reference is what flows into state,
// outputs, logs, audit and plan diff, so the value never enters durable data.
// It is resolved to the real value only at execution, from the request body,
// by secret.InputResolver.
//
// A non-sensitive variable binds to its input value, falling back to its
// default; a variable with neither is absent from the bindings.
func VariableBindings(vars []VariableBlock, inputs map[string]string) (map[string]cty.Value, error) {
	bindings := make(map[string]cty.Value)
	for _, vb := range vars {
		if vb.Sensitive {
			bindings[vb.Name] = cty.StringVal(secret.Input(vb.Name).String())
			continue
		}
		if input, ok := inputs[vb.Name]; ok {
			bindings[vb.Name] = cty.StringVal(input)
			continue
		}
		value, ok, err := variableDefault(vb)
		if err != nil {
			return nil, err
		}
		if ok {
			bindings[vb.Name] = value
		}
	}
	return bindings, nil
}

// variableDefault evaluates a variable block's default expression, if any.
func variableDefault(vb VariableBlock) (cty.Value, bool, error) {
	if vb.Remain == nil {
		return cty.NilVal, false, nil
	}
	attrs, diags := vb.Remain.JustAttributes()
	if diags.HasErrors() {
		return cty.NilVal, false, fmt.Errorf("variable %q: %s", vb.Name, diags.Error())
	}
	defAttr, ok := attrs["default"]
	if !ok {
		return cty.NilVal, false, nil
	}
	val, valueDiags := defAttr.Expr.Value(nil)
	if valueDiags.HasErrors() {
		return cty.NilVal, false, fmt.Errorf("variable %q default: %s", vb.Name, valueDiags.Error())
	}
	return val, true, nil
}
