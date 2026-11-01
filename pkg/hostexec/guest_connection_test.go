package hostexec

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// guestConnectionNode supplies only the Node method exercised by guest execution.
type guestConnectionNode struct {
	driver.Node
	connection substrate.Connection
}

func (n guestConnectionNode) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	return n.connection, nil
}

type legacyGuestConnection struct{ exec driver.GuestExec }

func (c legacyGuestConnection) Exec(ctx context.Context, req substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	return c.exec.ExecInNode(ctx, substrate.NodeHandle{}, req)
}

type fixedGuestConnection struct {
	result  substrate.ExecResult
	err     error
	request substrate.ExecRequest
}

func (c *fixedGuestConnection) Exec(_ context.Context, req substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	c.request = req
	return c.result, c.err
}

func TestGuestAPIAndChecksUseNodeConnection(t *testing.T) {
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	legacy := &failingGuestDriver{err: errors.New("legacy execution path must not run")}
	conn := &fixedGuestConnection{result: substrate.ExecResult{Stdout: "bounded", ExitCode: 0, ExitCodeKnown: true, StdoutTruncated: true}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", Node: guestConnectionNode{connection: conn}, GuestExec: legacy, NodeState: guestCodec{}}))
	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{Argv: []string{"printf", "hello"}})
	require.Equal(t, controlplane.GuestExecutionResultClassExit, result.ResultClass)
	require.True(t, result.Result.Truncated, "transport truncation must survive API encoding")
	require.Equal(t, controlplane.GuestOutputLimit, conn.request.OutputLimitBytes)
	root, eval := parseChecks(t, checkNodeHCL+`
check "health" {
 data "sysbox_exec" "probe" {
  node = sysbox_node.web.id
  argv = ["true"]
 }
 assert {
  condition = data.sysbox_exec.probe.exit_code == 0
  error_message = "unhealthy"
 }
}`)
	checks := EvaluateChecks(context.Background(), guestState(t, "guest-test"), root.Checks, eval)
	require.Empty(t, checks.FailedChecks)
	require.Equal(t, "true", conn.request.Program)
}

func TestGuestExecEmptyArgvReturnsValidationError(t *testing.T) {
	_, err := RunGuestExec(context.Background(), guestState(t, "guest-test"), "web", nil, nil, "")
	require.Error(t, err)
}

func TestGuestOperationDoesNotTurnUnknownExitIntoSuccess(t *testing.T) {
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	conn := &fixedGuestConnection{result: substrate.ExecResult{Stdout: "partial"}}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", Node: guestConnectionNode{connection: conn}, GuestExec: &guestTestDriver{}, NodeState: guestCodec{}}))
	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{Argv: []string{"true"}})
	require.Equal(t, "provider", result.ResultClass)
}
