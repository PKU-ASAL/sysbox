package guesttask

import (
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestConsoleCommandIsOneUnprivilegedTerminalTask(t *testing.T) {
	c := controlplane.AgentCommand{ID: "open", Type: "session_open", ActionID: "session", TopologyUID: "topology", ResourceUID: "node", Generation: 1, Session: &controlplane.ConsoleSession{ID: "session", Topology: "lab", Node: "web"}, Request: controlplane.ConsoleRequest{Cols: 120, Rows: 32}}
	r, err := FromConsoleCommand(c)
	require.NoError(t, err)
	require.Equal(t, uint32(65534), r.UserID)
	require.Equal(t, []string{"/bin/sh"}, r.Args)
	require.Equal(t, c.ActionID, r.ID)
	require.True(t, r.Terminal.TTY)
	c.Request.Cmd = []string{"/bin/cat"}
	c.Request.Env = map[string]string{"TERM": "xterm"}
	a, err := FromConsoleCommand(c)
	require.NoError(t, err)
	b, err := a.Digest()
	require.NoError(t, err)
	c.Request.Rows++
	a, err = FromConsoleCommand(c)
	require.NoError(t, err)
	d, err := a.Digest()
	require.NoError(t, err)
	require.NotEqual(t, b, d)
	c.ActionID = "other"
	_, err = FromConsoleCommand(c)
	require.Error(t, err)
}
