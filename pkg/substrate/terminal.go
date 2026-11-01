package substrate

import (
	"context"
	"fmt"
)

// TerminalOptions opts a durable task into live input. A nil value preserves
// the finite stdin and separate output streams of an ordinary guest task.
type TerminalOptions struct {
	TTY  bool `json:"tty"`
	Cols int  `json:"cols,omitempty"`
	Rows int  `json:"rows,omitempty"`
}

func (t TerminalOptions) Validate() error {
	if t.TTY {
		return validateTerminalSize(t.Cols, t.Rows)
	}
	if t.Cols != 0 || t.Rows != 0 {
		return fmt.Errorf("terminal dimensions require a TTY")
	}
	return nil
}

func validateTerminalSize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("terminal dimensions must be between 1 and 65535")
	}
	return nil
}

const MaxTerminalInput = 8192

// TerminalControl is never persisted. EOF closes a pipe's input; a PTY must
// receive the application's own EOF control character through stdin.
type TerminalControl struct {
	Type string `json:"type"`
	Data []byte `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (c TerminalControl) Validate() error {
	switch c.Type {
	case "stdin":
		if len(c.Data) > 0 && len(c.Data) <= MaxTerminalInput && c.Cols == 0 && c.Rows == 0 {
			return nil
		}
	case "resize":
		if len(c.Data) == 0 {
			return validateTerminalSize(c.Cols, c.Rows)
		}
	case "eof":
		if len(c.Data) == 0 && c.Cols == 0 && c.Rows == 0 {
			return nil
		}
	}
	return fmt.Errorf("invalid terminal control")
}

type ConnectionTerminal interface {
	ControlExecution(context.Context, ExecutionHandle, TerminalControl) error
}
