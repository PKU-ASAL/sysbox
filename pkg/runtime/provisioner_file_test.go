package runtime

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestRunProvisionersFileResolvesRelativeSourceAgainstWorkspace(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(nil, nil)
	e.SetWorkspaceDir(dir)

	conn := &recordingConn{}
	err := e.runProvisioners(context.Background(), conn, []config.ProvisionerConfig{
		{Type: "file", Source: "files/edge.py", Destination: "/opt/edge.py"},
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "files", "edge.py"), conn.copiedSrc)
}

type unknownExitConn struct{ recordingConn }

func (*unknownExitConn) Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error) {
	return substrate.ExecResult{Status: substrate.ExecStatusUnknown}, nil
}
func TestProvisionerUnknownExitIsNotSuccess(t *testing.T) {
	e := NewExecutor(nil, nil)
	require.ErrorIs(t, e.runProvisioners(context.Background(), &unknownExitConn{}, []config.ProvisionerConfig{{Type: "exec", Program: "true", Shell: "none"}}), ErrActionUncertain)
}

type startOnlyConn struct {
	recordingConn
	starts int
}

func (c *startOnlyConn) Start(context.Context, substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	c.starts++
	return substrate.ExecutionHandle{ID: "execution-1", NodeID: "node", Generation: 1, PID: 42}, nil
}
func TestProvisionerBackgroundUsesOpaqueExecutionHandle(t *testing.T) {
	e := NewExecutor(nil, nil)
	c := &startOnlyConn{}
	require.NoError(t, e.runProvisioners(context.Background(), c, []config.ProvisionerConfig{{Type: "exec", Program: "worker", Shell: "none", Background: true}}))
	require.Equal(t, 1, c.starts)
}

func TestRunProvisionersFileKeepsAbsoluteSource(t *testing.T) {
	e := NewExecutor(nil, nil)
	e.SetWorkspaceDir(t.TempDir())

	conn := &recordingConn{}
	err := e.runProvisioners(context.Background(), conn, []config.ProvisionerConfig{
		{Type: "file", Source: "/abs/edge.py", Destination: "/opt/edge.py"},
	})
	require.NoError(t, err)
	require.Equal(t, "/abs/edge.py", conn.copiedSrc)
}
