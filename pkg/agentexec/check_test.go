package agentexec

import (
	"context"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
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

func registerGuestExecDriver(t *testing.T, d *guestTestDriver) {
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
