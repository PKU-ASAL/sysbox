package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
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
			value, err := coerceInputToType(vb, input)
			if err != nil {
				return nil, err
			}
			bindings[vb.Name] = value
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

// InjectVariables binds var.<name> into an existing eval context from the
// variable blocks and the apply-time inputs. Sensitive variables bind to their
// secret://input/<name> reference, so the plaintext never enters the context
// (and therefore never enters state, outputs, logs, audit or plan diff).
func InjectVariables(ctx *hcl.EvalContext, vars []VariableBlock, inputs map[string]string) error {
	bindings, err := VariableBindings(vars, inputs)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	ctx.Variables["var"] = cty.ObjectVal(bindings)
	return nil
}

// variableTypeConstraint evaluates a variable's declared `type = ...` to a cty
// type. Only the primitive type names are understood today; a complex type
// (list/map/object) or an unrecognized name yields ok=false, so the input is
// left as a string (the pre-typing behavior).
func variableTypeConstraint(vb VariableBlock) (cty.Type, bool) {
	if vb.Type == nil {
		return cty.NilType, false
	}
	traversal, diags := hcl.AbsTraversalForExpr(vb.Type)
	if diags.HasErrors() || len(traversal) != 1 {
		return cty.NilType, false
	}
	switch traversal.RootName() {
	case "number":
		return cty.Number, true
	case "string":
		return cty.String, true
	default:
		return cty.NilType, false
	}
}

// coerceInputToType converts an apply-time input string to the variable's
// declared type. `number` is parsed so that count/for_each can consume a
// numeric input; any other (or no) declared type leaves the value as a string.
func coerceInputToType(vb VariableBlock, input string) (cty.Value, error) {
	typ, ok := variableTypeConstraint(vb)
	if !ok || typ != cty.Number {
		return cty.StringVal(input), nil
	}
	n, err := cty.ParseNumberVal(input)
	if err != nil {
		return cty.NilVal, fmt.Errorf("variable %q: %q is not a number: %w", vb.Name, input, err)
	}
	return n, nil
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

// PreflightVariableBindings builds a var.<name> object for no-inputs contexts
// (preflight, destroy, refresh, outputs, evaluation). A sensitive variable
// binds to its secret reference, a variable with a default binds to the
// default, and a variable with neither binds to a type-shaped placeholder so
// expressions that reference it still evaluate — the concrete value is supplied
// as an input on the apply path.
func PreflightVariableBindings(vars []VariableBlock) (map[string]cty.Value, error) {
	bindings, err := VariableBindings(vars, nil)
	if err != nil {
		return nil, err
	}
	for _, vb := range vars {
		if _, ok := bindings[vb.Name]; ok {
			continue
		}
		bindings[vb.Name] = variablePlaceholder(vb)
	}
	return bindings, nil
}

// variablePlaceholder returns a type-shaped zero value for a variable that has
// neither an input nor a default, so no-inputs contexts can still evaluate
// expressions referencing it.
func variablePlaceholder(vb VariableBlock) cty.Value {
	if typ, ok := variableTypeConstraint(vb); ok && typ == cty.Number {
		return cty.NumberIntVal(0)
	}
	return cty.StringVal("")
}
