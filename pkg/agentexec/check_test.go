package agentexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

const checkNodeHCL = `
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}
`

func parseChecks(t *testing.T, src string) (*config.Root, *hcl.EvalContext) {
	t.Helper()
	root, err := config.ParseString(src, "test.hcl")
	require.NoError(t, err)
	require.NoError(t, config.ValidateChecks(root.Checks))
	evalCtx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	return root, evalCtx
}

func registerGuestExecDriver(t *testing.T, d driver.GuestExec) {
	t.Helper()
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", GuestExec: d, NodeState: guestCodec{}}))
}

// A check whose data probe matches the author's assertion passes: no failed
// checks, no message.
func TestEvaluateChecksPassesWhenConditionHolds(t *testing.T) {
	root, evalCtx := parseChecks(t, checkNodeHCL+`
check "portal" {
  data "sysbox_exec" "health" {
    node = sysbox_node.web.id
    argv = ["curl", "http://127.0.0.1:8080/health"]
  }
  assert {
    condition     = data.sysbox_exec.health.exit_code == 7
    error_message = "portal not ready"
  }
}
`)
	registerGuestExecDriver(t, &guestTestDriver{}) // returns exit code 7

	result := evaluateChecks(context.Background(), guestState(t, "guest-test"), root.Checks, evalCtx)

	require.Empty(t, result.FailedChecks)
	require.Empty(t, result.Message)
}

// A check whose assertion is violated fails, carrying the author's error
// message with the probe value interpolated into it.
func TestEvaluateChecksFailsWithAuthorMessage(t *testing.T) {
	root, evalCtx := parseChecks(t, checkNodeHCL+`
check "portal" {
  data "sysbox_exec" "health" {
    node = sysbox_node.web.id
    argv = ["curl", "http://127.0.0.1:8080/health"]
  }
  assert {
    condition     = data.sysbox_exec.health.exit_code == 0
    error_message = "portal not ready: exit=${data.sysbox_exec.health.exit_code}"
  }
}
`)
	registerGuestExecDriver(t, &guestTestDriver{}) // returns exit code 7

	result := evaluateChecks(context.Background(), guestState(t, "guest-test"), root.Checks, evalCtx)

	require.Equal(t, []string{"portal"}, result.FailedChecks)
	require.Contains(t, result.Message, "portal not ready")
	require.Contains(t, result.Message, "7", "the probe's exit code must be interpolated into the author's message")
}

// The eval context exposes the probe as data.<type>.<name>, which is what makes
// the assert condition expression resolvable.
func TestCheckDataContextExposesData(t *testing.T) {
	ctx := checkDataContext("sysbox_exec", "health", cty.ObjectVal(map[string]cty.Value{
		"exit_code": cty.NumberIntVal(0),
	}))

	data, ok := ctx.Variables["data"]
	require.True(t, ok)
	val := data.GetAttr("sysbox_exec").GetAttr("health").GetAttr("exit_code")
	require.Equal(t, cty.NumberIntVal(0), val)
}

// recordingDriver records the argv it was asked to execute and returns a fixed
// exit code, so a test can verify the probe command sysbox_reach issued.
type recordingDriver struct {
	argv     []string
	exitCode int
}

func (d *recordingDriver) ExecInNode(_ context.Context, _ substrate.NodeHandle, req substrate.ExecRequest) (substrate.ExecResult, error) {
	d.argv = append([]string{req.Program}, req.Args...)
	return substrate.ExecResult{ExitCode: d.exitCode}, nil
}
func (*recordingDriver) ExecBackground(context.Context, substrate.NodeHandle, substrate.ExecRequest) (int, error) {
	return 0, nil
}

// reachState builds a state with two nodes; core has a primary_ip so that a
// reach probe can target it.
func reachState(t *testing.T) *state.State {
	t.Helper()
	st := &state.State{}
	for _, r := range []state.Resource{
		{Address: address.Resource("sysbox_node", "edge"), Driver: "guest-test"},
		{Address: address.Resource("sysbox_node", "core"), Driver: "guest-test", Attributes: state.MustAttributes(map[string]any{"primary_ip": "10.0.0.5"})},
	} {
		require.NoError(t, r.SetProviderState(json.RawMessage(`{"id":"opaque"}`)))
		st.Resources = append(st.Resources, r)
	}
	return st
}

// sysbox_reach probes the target's address from the source node via the
// guest-exec primitive, and reports reachable as the probe's exit status.
func TestEvaluateChecksReachProbesFromNode(t *testing.T) {
	root, evalCtx := parseChecks(t, `
resource "sysbox_node" "edge" {
  image     = "alpine"
  substrate = "docker"
}
resource "sysbox_node" "core" {
  image     = "alpine"
  substrate = "docker"
}

check "isolation" {
  data "sysbox_reach" "edge_core" {
    from = sysbox_node.edge.id
    to   = sysbox_node.core.id
    port = 5432
  }
  assert {
    condition     = data.sysbox_reach.edge_core.reachable == false
    error_message = "edge must not reach core"
  }
}
`)
	d := &recordingDriver{exitCode: 0} // probe succeeds → reachable true → assertion (reachable==false) fails
	registerGuestExecDriver(t, d)

	result := evaluateChecks(context.Background(), reachState(t), root.Checks, evalCtx)

	require.Equal(t, []string{"nc", "-z", "-w", "2", "10.0.0.5", "5432"}, d.argv,
		"the probe must run from the source node against the target's state address")
	require.Equal(t, []string{"isolation"}, result.FailedChecks,
		"reachable=true violates the assertion reachable==false, so the check fails")
}

// The same composition, with the target unreachable: the probe exits non-zero,
// so reachable is false and the negative assertion holds.
func TestEvaluateChecksReachUnreachablePassesNegativeAssertion(t *testing.T) {
	root, evalCtx := parseChecks(t, `
resource "sysbox_node" "edge" {
  image     = "alpine"
  substrate = "docker"
}
resource "sysbox_node" "core" {
  image     = "alpine"
  substrate = "docker"
}

check "isolation" {
  data "sysbox_reach" "edge_core" {
    from = sysbox_node.edge.id
    to   = sysbox_node.core.id
    port = 5432
  }
  assert {
    condition     = data.sysbox_reach.edge_core.reachable == false
    error_message = "edge must not reach core"
  }
}
`)
	d := &recordingDriver{exitCode: 1} // probe fails → reachable false → assertion holds
	registerGuestExecDriver(t, d)

	result := evaluateChecks(context.Background(), reachState(t), root.Checks, evalCtx)

	require.Empty(t, result.FailedChecks)
}

func TestExecDataTimeout(t *testing.T) {
	require.Equal(t, 30*time.Second, execDataTimeout(0))
	require.Equal(t, 30*time.Second, execDataTimeout(-1))
	require.Equal(t, 5*time.Second, execDataTimeout(5))
}

// A sysbox_exec data source accepts an optional timeout (seconds); it must
// decode without tripping ValidateChecks.
func TestExecDataTimeoutFieldDecodes(t *testing.T) {
	root, _ := parseChecks(t, checkNodeHCL+`
check "portal" {
  data "sysbox_exec" "health" {
    node    = sysbox_node.web.id
    argv    = ["curl", "http://127.0.0.1:8080/health"]
    timeout = 12
  }
  assert {
    condition     = data.sysbox_exec.health.exit_code == 0
    error_message = "portal not ready"
  }
}
`)
	require.Len(t, root.Checks, 1)
}
