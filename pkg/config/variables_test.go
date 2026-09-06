package config

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func varBlock(t *testing.T, name, body string) VariableBlock {
	t.Helper()
	vb := parseVariable(t, body)
	vb.Name = name
	return *vb
}

// A sensitive variable is bound to its secret://input/<name> reference, never
// to the plaintext input value. That is what keeps the value out of state,
// outputs, logs, audit and plan diff — those all carry the reference string.
func TestVariableBindingsSensitiveIsReference(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "flag", "sensitive = true")}

	bindings, err := VariableBindings(vars, map[string]string{"flag": "the-secret"})

	require.NoError(t, err)
	require.Equal(t, "secret://input/flag", bindings["flag"].AsString())
	require.NotContains(t, bindings["flag"].AsString(), "the-secret")
}

// A non-sensitive variable binds to its input value.
func TestVariableBindingsNonSensitiveUsesInput(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "cidr", "")}

	bindings, err := VariableBindings(vars, map[string]string{"cidr": "10.0.0.0/24"})

	require.NoError(t, err)
	require.Equal(t, "10.0.0.0/24", bindings["cidr"].AsString())
}

// A non-sensitive variable without an input falls back to its default.
func TestVariableBindingsNonSensitiveUsesDefault(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "cidr", `default = "10.0.0.0/24"`)}

	bindings, err := VariableBindings(vars, nil)

	require.NoError(t, err)
	require.Equal(t, "10.0.0.0/24", bindings["cidr"].AsString())
}

// Input wins over default for a non-sensitive variable.
func TestVariableBindingsInputOverridesDefault(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "cidr", `default = "10.0.0.0/24"`)}

	bindings, err := VariableBindings(vars, map[string]string{"cidr": "10.9.9.0/24"})

	require.NoError(t, err)
	require.Equal(t, "10.9.9.0/24", bindings["cidr"].AsString())
}

// A variable with no input and no default is simply absent from the bindings.
func TestVariableBindingsNoValueIsAbsent(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "image", "")}

	bindings, err := VariableBindings(vars, nil)

	require.NoError(t, err)
	_, ok := bindings["image"]
	require.False(t, ok)
}

// InjectVariables puts the var.<name> bindings into an existing eval context.
func TestInjectVariablesAddsVarNamespace(t *testing.T) {
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{}}
	vars := []VariableBlock{varBlock(t, "cidr", ""), varBlock(t, "flag", "sensitive = true")}

	require.NoError(t, InjectVariables(ctx, vars, map[string]string{"cidr": "10.0.0.0/24", "flag": "secret"}))

	var v cty.Value
	var ok bool
	if v, ok = ctx.Variables["var"]; !ok {
		t.Fatal("var namespace not injected")
	}
	require.Equal(t, "10.0.0.0/24", v.GetAttr("cidr").AsString())
	require.Equal(t, "secret://input/flag", v.GetAttr("flag").AsString())
}

// A variable declared `type = number` coerces a numeric apply input from its
// string wire form, so count/for_each can consume it.
func TestVariableBindingsCoercesNumberInput(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "n", "type = number")}

	bindings, err := VariableBindings(vars, map[string]string{"n": "2"})

	require.NoError(t, err)
	require.Equal(t, cty.Number, bindings["n"].Type())
	n, _ := bindings["n"].AsBigFloat().Int64()
	require.Equal(t, int64(2), n)
}

// A non-numeric input for a `type = number` variable is rejected, not silently
// coerced to a string.
func TestVariableBindingsRejectsNonNumericInput(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "n", "type = number")}

	_, err := VariableBindings(vars, map[string]string{"n": "not-a-number"})

	require.ErrorContains(t, err, "not a number")
}

// Without a declared type the input stays a string (the pre-typing behavior).
func TestVariableBindingsUntypedInputStaysString(t *testing.T) {
	vars := []VariableBlock{varBlock(t, "n", "")}

	bindings, err := VariableBindings(vars, map[string]string{"n": "2"})

	require.NoError(t, err)
	require.Equal(t, cty.String, bindings["n"].Type())
	require.Equal(t, "2", bindings["n"].AsString())
}
