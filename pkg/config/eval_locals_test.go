package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// locals 可以引用 var.*、先声明的 local.*、substrate.*。
func TestLocalsReferenceVarAndEarlierLocalAndSubstrate(t *testing.T) {
	root, err := ParseString(`
variable "team_index" { type = number }
substrate "docker" {
  alias = "local"
}

locals {
  subnet = cidrsubnet("10.200.0.0/16", 8, var.team_index)
  web_ip = cidrhost(local.subnet, 10)
  which  = substrate.docker.local
}
resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = "alpine"
}
`, "locals.hcl")
	require.NoError(t, err)

	ctx, err := BuildEvalContextWithInputs(root, "", map[string]string{"team_index": "3"})
	require.NoError(t, err)

	locals := ctx.Variables["local"]
	require.Equal(t, "10.200.3.0/24", locals.GetAttr("subnet").AsString())
	require.Equal(t, "10.200.3.10", locals.GetAttr("web_ip").AsString())
	require.Equal(t, "docker", locals.GetAttr("which").AsString())
}

// count 可以引用 substrate.*（经 locals）。
func TestCountReferencesSubstrate(t *testing.T) {
	root, err := ParseString(`
substrate "docker" {
  alias = "local"
}
locals { n = substrate.docker.local == "docker" ? 2 : 0 }
resource "sysbox_node" "web" {
  count     = local.n
  substrate = substrate.docker.local
  image     = "alpine"
}
`, "count.hcl")
	require.NoError(t, err)

	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	nodes := ctx.Variables["sysbox_node"].GetAttr("web")
	require.True(t, nodes.Type().IsTupleType(), "count-expanded resource should be a tuple, got %s", nodes.Type().FriendlyName())
	require.Equal(t, 2, nodes.LengthInt())
}
