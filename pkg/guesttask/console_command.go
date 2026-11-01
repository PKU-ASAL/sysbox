package guesttask

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// FromConsoleCommand binds the terminal to its admitted session. Console uses
// the same unprivileged task supervisor and signed payload as guest execution.
func FromConsoleCommand(cmd controlplane.AgentCommand) (Request, error) {
	if cmd.Type != "session_open" || cmd.Session == nil || cmd.Session.ID == "" || cmd.ActionID != cmd.Session.ID || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 {
		return Request{}, fmt.Errorf("console command requires an exact session and resource identity")
	}
	request := cmd.Request
	args := request.Cmd
	if len(args) == 0 {
		shell := request.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		args = []string{shell}
	}
	terminal := &substrate.TerminalOptions{TTY: request.TTY == nil || *request.TTY}
	if terminal.TTY {
		terminal.Cols, terminal.Rows = request.Cols, request.Rows
	}
	task := Request{UserID: 65534, ID: cmd.ActionID, Args: args, Environment: request.Env, WorkingDir: request.WorkDir, OutputLimit: substrate.DefaultExecOutputLimit, Terminal: terminal}
	return task, task.Validate()
}
