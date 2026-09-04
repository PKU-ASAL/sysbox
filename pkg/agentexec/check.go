package agentexec

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

// evaluateChecks runs the topology's check blocks after apply and returns the
// assertion outcome: the names of the checks that failed and the first failure
// message. An empty result means every check passed (or there were no checks).
//
// Checks run after apply because their probes — a command inside a node, a
// reachability probe between nodes — can only run against a built topology.
// They never enter the dependency graph, so this is a separate post-apply phase,
// not part of planning.
func evaluateChecks(ctx context.Context, st *state.State, checks []config.CheckBlock, evalCtx *hcl.EvalContext) controlplane.AssertionResult {
	var result controlplane.AssertionResult
	for _, check := range checks {
		if len(check.Data) == 0 {
			continue // nothing to probe
		}
		data := check.Data[0]

		dataVal, err := resolveCheckData(ctx, st, data, evalCtx)
		if err != nil {
			// The probe itself failed; the check cannot pass.
			result.FailedChecks = append(result.FailedChecks, check.Name)
			if result.Message == "" {
				result.Message = err.Error()
			}
			continue
		}

		checkCtx := checkDataContext(data.Type, data.Name, dataVal)
		for _, assert := range check.Asserts {
			failed, message, err := evaluateAssert(assert, checkCtx)
			if err != nil {
				result.FailedChecks = append(result.FailedChecks, check.Name)
				if result.Message == "" {
					result.Message = err.Error()
				}
				break
			}
			if failed {
				result.FailedChecks = append(result.FailedChecks, check.Name)
				if result.Message == "" {
					result.Message = message
				}
				break
			}
		}
	}
	return result
}

// resolveCheckData evaluates a check's scoped data source into a cty value that
// the assert conditions can reference as data.<type>.<name>.
func resolveCheckData(ctx context.Context, st *state.State, d config.DataBlock, evalCtx *hcl.EvalContext) (cty.Value, error) {
	switch d.Type {
	case "sysbox_exec":
		return resolveExecData(ctx, st, d, evalCtx)
	case "sysbox_reach":
		return resolveReachData(ctx, st, d, evalCtx)
	default:
		return cty.NilVal, fmt.Errorf("check data source %q is not supported", d.Type)
	}
}

func resolveExecData(ctx context.Context, st *state.State, d config.DataBlock, evalCtx *hcl.EvalContext) (cty.Value, error) {
	cfg := &config.DataExecConfig{}
	if diag := gohcl.DecodeBody(d.Remain, evalCtx, cfg); diag.HasErrors() {
		return cty.NilVal, fmt.Errorf("data %s.%s: %s", d.Type, d.Name, diag.Error())
	}
	nodeAddr, err := config.ResolveResourceAddress(cfg.Node, "sysbox_node")
	if err != nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: %w", d.Type, d.Name, err)
	}

	result, err := runGuestExec(ctx, st, nodeAddr.Name, cfg.Argv, nil, "")
	if err != nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: %w", d.Type, d.Name, err)
	}

	truncated := len(result.Stdout) > guestOutputLimit || len(result.Stderr) > guestOutputLimit
	return cty.ObjectVal(map[string]cty.Value{
		"exit_code": cty.NumberIntVal(int64(result.ExitCode)),
		"stdout":    cty.StringVal(result.Stdout),
		"stderr":    cty.StringVal(result.Stderr),
		"truncated": cty.BoolVal(truncated),
	}), nil
}

func resolveReachData(ctx context.Context, st *state.State, d config.DataBlock, evalCtx *hcl.EvalContext) (cty.Value, error) {
	cfg := &config.DataReachConfig{}
	if diag := gohcl.DecodeBody(d.Remain, evalCtx, cfg); diag.HasErrors() {
		return cty.NilVal, fmt.Errorf("data %s.%s: %s", d.Type, d.Name, diag.Error())
	}
	fromAddr, err := config.ResolveResourceAddress(cfg.From, "sysbox_node")
	if err != nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: %w", d.Type, d.Name, err)
	}
	toAddr, err := config.ResolveResourceAddress(cfg.To, "sysbox_node")
	if err != nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: %w", d.Type, d.Name, err)
	}
	toRes := st.FindResource(toAddr)
	if toRes == nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: node %s not in state", d.Type, d.Name, toAddr.String())
	}
	toIP := toRes.Str("primary_ip")
	if toIP == "" {
		return cty.NilVal, fmt.Errorf("data %s.%s: node %s has no primary_ip", d.Type, d.Name, toAddr.String())
	}

	// sysbox_reach is composition, not a new capability: look up the target's
	// address in state, then probe it from the source via the guest-exec
	// primitive. A TCP connect from the source is the reachability signal.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	argv := []string{"nc", "-z", "-w", "2", toIP, strconv.Itoa(cfg.Port)}
	result, err := runGuestExec(probeCtx, st, fromAddr.Name, argv, nil, "")
	if err != nil {
		return cty.NilVal, fmt.Errorf("data %s.%s: probe: %w", d.Type, d.Name, err)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"reachable": cty.BoolVal(result.ExitCode == 0),
	}), nil
}

// checkDataContext builds the eval context in which a check's assert conditions
// are evaluated, exposing the resolved probe as data.<type>.<name>.
func checkDataContext(dataType, dataName string, dataVal cty.Value) *hcl.EvalContext {
	data := cty.ObjectVal(map[string]cty.Value{
		dataType: cty.ObjectVal(map[string]cty.Value{
			dataName: dataVal,
		}),
	})
	return &hcl.EvalContext{Variables: map[string]cty.Value{"data": data}}
}

// evaluateAssert evaluates one assert block's condition. It returns failed=true
// with the author-written error_message when the condition is false, or an
// error when the assert is malformed (missing condition, non-boolean, bad
// expression).
func evaluateAssert(assert config.AssertBlock, evalCtx *hcl.EvalContext) (failed bool, message string, err error) {
	if assert.Remain == nil {
		return false, "", fmt.Errorf("assert block is empty")
	}
	attrs, diags := assert.Remain.JustAttributes()
	if diags.HasErrors() {
		return false, "", fmt.Errorf("assert: %s", diags.Error())
	}

	condAttr, ok := attrs["condition"]
	if !ok {
		return false, "", fmt.Errorf("assert is missing its condition")
	}
	condVal, diags := condAttr.Expr.Value(evalCtx)
	if diags.HasErrors() {
		return false, "", fmt.Errorf("condition: %s", diags.Error())
	}
	if condVal.Type() != cty.Bool {
		return false, "", fmt.Errorf("condition must be a boolean, got %s", condVal.Type().FriendlyName())
	}
	if condVal.True() {
		return false, "", nil
	}

	message = ""
	if msgAttr, ok := attrs["error_message"]; ok {
		msgVal, msgDiags := msgAttr.Expr.Value(evalCtx)
		if !msgDiags.HasErrors() && msgVal.Type() == cty.String {
			message = msgVal.AsString()
		}
	}
	return true, message, nil
}
