package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvFunctionProducesReferenceWithoutReadingPlaintext(t *testing.T) {
	t.Setenv("CANARY_TOKEN", "plaintext-canary-must-not-persist")
	root, err := ParseString(`locals { token = env("CANARY_TOKEN") }`, "secret.hcl")
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)
	require.Equal(t, "secret://env/CANARY_TOKEN", ctx.Variables["local"].GetAttr("token").AsString())
}
