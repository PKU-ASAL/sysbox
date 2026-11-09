package hostexec

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type hintRecordingNode struct {
	guestConnectionNode
	hints []substrate.ConnectionHint
}

func (d *hintRecordingNode) Connection(_ substrate.NodeHandle, h []substrate.ConnectionHint) (substrate.Connection, error) {
	d.hints = h
	return d, nil
}
func (d *hintRecordingNode) Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error) {
	return substrate.ExecResult{ExitCodeKnown: true}, nil
}
func (d *hintRecordingNode) ExecInNode(context.Context, substrate.NodeHandle, substrate.ExecRequest) (substrate.ExecResult, error) {
	return substrate.ExecResult{}, nil
}
func TestForegroundChecksUseCanonicalConnectionHints(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	d := &hintRecordingNode{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "hint-test", Version: "1", Node: d, NodeState: guestCodec{}, GuestExec: d}))
	st := guestState(t, "hint-test")
	st.Resources[0].Attributes = state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"Type": "winrm", "Host": "https://192.0.2.10:5986/wsman", "User": "operator", "Password": "secret://input/password"}}}})
	ctx := secret.WithResolver(context.Background(), secret.InputResolver{Inputs: map[string]string{"password": "transient"}})
	_, err := RunGuestExec(ctx, st, "web", []string{"powershell.exe", "-Command", "exit 0"}, nil, "")
	require.NoError(t, err)
	require.Len(t, d.hints, 1)
	require.Equal(t, "winrm", d.hints[0].Type)
	require.Equal(t, "transient", d.hints[0].Password)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "transient")
}

func TestRecordAssertionResolvesRunInputsForForegroundConnection(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	d := &hintRecordingNode{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "hint-test", Version: "1", Node: d, NodeState: guestCodec{}, GuestExec: d}))
	st := guestState(t, "hint-test")
	st.Resources[0].Attributes = state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []map[string]any{{"Type": "winrm", "Host": "https://192.0.2.10:5986/wsman", "User": "operator", "Password": "secret://input/password"}}}})
	root, eval := parseChecks(t, checkNodeHCL+`
check "ready" {
 data "sysbox_exec" "probe" {
 node=sysbox_node.web.id
 argv=["powershell.exe","-Command","exit 0"]
 }
 assert {
 condition=data.sysbox_exec.probe.exit_code==0
 error_message="guest unavailable"
 }
}`)
	op := &runtime.Operation{State: st, Root: root, EvalContext: eval}
	run := &controlplane.Run{Inputs: map[string]string{"password": "private-canary"}}
	recordAssertion(context.Background(), op, run, io.Discard)
	require.NotNil(t, run.Assertion)
	require.Empty(t, run.Assertion.FailedChecks, run.Assertion.Message)
	require.Len(t, d.hints, 1)
	require.Equal(t, "private-canary", d.hints[0].Password)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-canary")
	run.Inputs = nil
	d.hints = nil
	recordAssertion(context.Background(), op, run, io.Discard)
	require.NotEmpty(t, run.Assertion.FailedChecks)
	require.Nil(t, d.hints, "missing secret must fail before constructing a connection")
}
