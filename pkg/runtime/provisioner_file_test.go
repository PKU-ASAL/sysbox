package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
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
