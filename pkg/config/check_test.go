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
    node = sysbox_node.portal.id
    argv = ["/usr/bin/curl", "-fsS", "http://127.0.0.1:8080/health"]
  }
  assert {
    condition     = data.sysbox_exec.health.exit_code == 0
    error_message = "portal /health not ready"
  }
}

check "edge_cannot_reach_core" {
  data "sysbox_reach" "edge_core" {
    from = sysbox_node.edge.id
    to   = sysbox_node.core.id
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
    node = sysbox_node.web.id
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

func TestValidateChecksAcceptsWellFormed(t *testing.T) {
	src := `
check "ok" {
  data "sysbox_exec" "probe" {
    node = sysbox_node.web.id
    argv = ["true"]
  }
  assert {
    condition     = data.sysbox_exec.probe.exit_code == 0
    error_message = "first"
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.NoError(t, ValidateChecks(root.Checks))
}

func TestValidateChecksRejectsTooManyDataBlocks(t *testing.T) {
	src := `
check "bad" {
  data "sysbox_exec" "a" {
    node = sysbox_node.web.id
    argv = ["true"]
  }
  data "sysbox_exec" "b" {
    node = sysbox_node.web.id
    argv = ["true"]
  }
  assert {
    condition     = true
    error_message = "x"
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.ErrorContains(t, ValidateChecks(root.Checks), "at most one data block")
}

func TestValidateChecksRejectsMissingAssert(t *testing.T) {
	src := `
check "bad" {
  data "sysbox_exec" "a" {
    node = sysbox_node.web.id
    argv = ["true"]
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.ErrorContains(t, ValidateChecks(root.Checks), "at least one assert")
}

func TestValidateChecksRejectsUnknownDataSource(t *testing.T) {
	src := `
check "bad" {
  data "sysbox_unknown" "a" {
    foo = "bar"
  }
  assert {
    condition     = true
    error_message = "x"
  }
}
`
	root, err := ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.ErrorContains(t, ValidateChecks(root.Checks), "unsupported data source")
}
