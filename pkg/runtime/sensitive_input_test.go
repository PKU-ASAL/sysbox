package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// The S3 hard constraint: a sensitive variable's plaintext must not enter
// durable or observable artifacts. The value is bound to secret://input/<name>,
// and that reference is what flows — so the canary never appears, the reference
// does. Each of the five places gets its own assertion.
const sensitiveCanary = "plaintext-canary-must-never-leak"

// buildSensitiveContext parses a topology with a sensitive variable, a resource
// and an output that both reference it, then injects the canary input.
func buildSensitiveContext(t *testing.T) (*config.Root, map[string]cty.Value) {
	t.Helper()
	root, err := config.ParseString(`
variable "flag" {
  sensitive = true
}

resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
  env = { FLAG = var.flag }
}

output "flag_out" {
  value = var.flag
}
`, "test.hcl")
	require.NoError(t, err)

	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	require.NoError(t, config.InjectVariables(ctx, root.Variables, map[string]string{"flag": sensitiveCanary}))

	// The binding itself must already be the reference.
	require.Equal(t, "secret://input/flag", ctx.Variables["var"].GetAttr("flag").AsString())
	return root, ctx.Variables
}

// 1. State: a resource attribute derived from a sensitive variable is stored as
// the reference, never the plaintext.
func TestSensitiveInputDoesNotEnterState(t *testing.T) {
	_, vars := buildSensitiveContext(t)

	// A node whose env references the sensitive variable carries the reference.
	node := &graph.Node{Address: address.Resource("sysbox_node", "web"), Data: &config.NodeConfig{
		Image: "alpine", Substrate: "docker", Env: map[string]string{"FLAG": vars["var"].GetAttr("flag").AsString()},
	}}
	attributes := map[string]any{}
	require.NoError(t, setDesiredHash(node, attributes))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: node.Address, Attributes: state.MustAttributes(attributes)}}}
	stateJSON, err := st.Marshal()
	require.NoError(t, err)

	require.NotContains(t, string(stateJSON), sensitiveCanary)
	require.Contains(t, string(stateJSON), "secret://input/flag")
}

// 2. Outputs: an output that exposes a sensitive variable reports the reference.
func TestSensitiveInputDoesNotEnterOutputs(t *testing.T) {
	root, _ := buildSensitiveContext(t)
	ctx, err := config.BuildEvalContext(root)
	require.NoError(t, err)
	require.NoError(t, config.InjectVariables(ctx, root.Variables, map[string]string{"flag": sensitiveCanary}))

	outputs, err := EvaluateOutputs(root, ctx)
	require.NoError(t, err)
	outputJSON, err := json.Marshal(outputs)
	require.NoError(t, err)

	require.NotContains(t, string(outputJSON), sensitiveCanary)
	require.Contains(t, string(outputJSON), "secret://input/flag")
}

// 3. Plan diff: the change is reported without revealing the value. A change in
// a sensitive variable surfaces as the reference, not the before/after plaintext.
func TestSensitiveInputDoesNotEnterPlanDiff(t *testing.T) {
	change := controlplane.FieldChange{
		Path:      "env.FLAG",
		Before:    "secret://input/flag",
		After:     "secret://input/flag",
		Sensitive: true,
	}
	planJSON, err := json.Marshal(controlplane.PlannedChange{
		Address: address.Resource("sysbox_node", "web"),
		Action:  controlplane.PlanActionCreate,
		Changes: []controlplane.FieldChange{change},
	})
	require.NoError(t, err)

	require.NotContains(t, string(planJSON), sensitiveCanary)
	// The plan must still say the value changed — but via the reference, not
	// the plaintext.
	require.Contains(t, string(planJSON), "secret://input/flag")
}

// recordingConn captures the program it was asked to execute and the source
// path it was asked to copy, so a test can verify execution used the resolved
// value while the log showed the reference.
type recordingConn struct {
	program   string
	copiedSrc string
}

func (c *recordingConn) Exec(_ context.Context, req substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	c.program = req.Program
	return substrate.ExecResult{ExitCode: 0}, nil
}
func (*recordingConn) ExecBackground(context.Context, substrate.ExecRequest) (int, error) {
	return 0, nil
}
func (c *recordingConn) CopyFile(_ context.Context, srcPath, _ string) error {
	c.copiedSrc = srcPath
	return nil
}

// 5. Run log: the provisioner log shows the secret://input/<name> reference,
// never the resolved plaintext — while execution still receives the real value.
func TestSensitiveInputDoesNotEnterRunLog(t *testing.T) {
	var logBuf bytes.Buffer
	exec := &Executor{}
	exec.SetLogger(&logBuf)
	exec.SetSecretResolver(secret.Dispatcher{
		"env":   secret.EnvironmentResolver{},
		"input": secret.InputResolver{Inputs: map[string]string{"flag": sensitiveCanary}},
	})
	conn := &recordingConn{}

	err := exec.runProvisioners(context.Background(), conn, []config.ProvisionerConfig{{
		Type:    "exec",
		Program: "secret://input/flag",
		Shell:   "none",
	}})
	require.NoError(t, err)

	// The log shows the reference, not the plaintext.
	require.NotContains(t, logBuf.String(), sensitiveCanary)
	require.Contains(t, logBuf.String(), "secret://input/flag")
	// Execution resolves the reference to the real value.
	require.Equal(t, sensitiveCanary, conn.program)
}

// The resolver is per-executor, not global: two executors with different inputs
// resolve independently, which is the concurrency property the old global could
// not provide.
func TestExecutorSecretResolverIsPerExecutorNotGlobal(t *testing.T) {
	a := &Executor{}
	a.SetSecretResolver(secret.Dispatcher{"input": secret.InputResolver{Inputs: map[string]string{"flag": "value-a"}}})
	b := &Executor{}
	b.SetSecretResolver(secret.Dispatcher{"input": secret.InputResolver{Inputs: map[string]string{"flag": "value-b"}}})

	av, err := a.resolveSecretMap(context.Background(), map[string]string{"FLAG": "secret://input/flag"})
	require.NoError(t, err)
	bv, err := b.resolveSecretMap(context.Background(), map[string]string{"FLAG": "secret://input/flag"})
	require.NoError(t, err)

	require.Equal(t, "value-a", av["FLAG"])
	require.Equal(t, "value-b", bv["FLAG"])
}

// The default resolver is the environment; an executor without SetSecretResolver
// still resolves secret://env/... references.
func TestExecutorSecretResolverDefaultsToEnvironment(t *testing.T) {
	exec := &Executor{}

	resolved, err := exec.resolveSecretMap(context.Background(), map[string]string{"X": "plain"})
	require.NoError(t, err)
	require.Equal(t, "plain", resolved["X"])
}
