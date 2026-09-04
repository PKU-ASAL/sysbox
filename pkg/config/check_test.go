package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A check block wraps a scoped data source and one or more assertions. The
// parser must accept it, including the two new data source types sysbox_exec
// and sysbox_reach, and the assert block's condition/error_message attributes.
func TestParseCheckBlock(t *testing.T) {
	src := `
check "portal_http" {
  data "sysbox_exec" "health" {
    node = sysbox_node.portal
    argv = ["/usr/bin/curl", "-fsS", "http://127.0.0.1:8080/health"]
  }
  assert {
    condition     = data.sysbox_exec.health.exit_code == 0
    error_message = "portal /health not ready"
  }
}

check "edge_cannot_reach_core" {
  data "sysbox_reach" "edge_core" {
    from = sysbox_node.edge
    to   = sysbox_node.core
    port = 5432
  }
  assert {
    condition     = data.sysbox_reach.edge_core.reachable == false
    error_message = "edge must not reach core:5432"
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)

	require.Len(t, root.Checks, 2)
	require.Equal(t, "portal_http", root.Checks[0].Name)
	require.Len(t, root.Checks[0].Data, 1)
	require.Equal(t, "sysbox_exec", root.Checks[0].Data[0].Type)
	require.Equal(t, "health", root.Checks[0].Data[0].Name)
	require.Len(t, root.Checks[0].Asserts, 1)

	require.Equal(t, "edge_cannot_reach_core", root.Checks[1].Name)
	require.Equal(t, "sysbox_reach", root.Checks[1].Data[0].Type)
}

// A check with a data block must carry at least one assert; the parser accepts
// the block shape regardless, but the assert attributes are what make it a
// check rather than a bare data lookup.
func TestParseCheckBlockWithMultipleAsserts(t *testing.T) {
	src := `
check "multi" {
  data "sysbox_exec" "probe" {
    node = sysbox_node.web
    argv = ["true"]
  }
  assert {
    condition     = data.sysbox_exec.probe.exit_code == 0
    error_message = "first"
  }
  assert {
    condition     = data.sysbox_exec.probe.exit_code < 10
    error_message = "second"
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.Len(t, root.Checks[0].Asserts, 2)
}
