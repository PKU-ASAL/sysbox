package transport

import (
	"context"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/creack/pty"

	"github.com/oslab/sysbox/pkg/substrate"
)

type ConsoleRequest struct {
	Cmd   []string
	Shell string
	Env   map[string]string
	Cols  int
	Rows  int
}

type RawConsoleSession struct {
	conn net.Conn
}

func NewRawConsoleSession(conn net.Conn) *RawConsoleSession {
	return &RawConsoleSession{conn: conn}
}

func (s *RawConsoleSession) Stdin() io.WriteCloser { return s.conn }
func (s *RawConsoleSession) Stdout() io.Reader     { return s.conn }
func (s *RawConsoleSession) Stderr() io.Reader     { return nil }
func (s *RawConsoleSession) Resize(context.Context, int, int) error {
	return nil
}
func (s *RawConsoleSession) Wait() (int, error) {
	buf := make([]byte, 1)
	for {
		if _, err := s.conn.Read(buf); err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 1, err
		}
	}
}
func (s *RawConsoleSession) Close() error { return s.conn.Close() }

type SSHConsoleSession struct {
	cmd  *osexec.Cmd
	ptmx *os.File
}

func NewSSHConsoleSession(ctx context.Context, sshArgs []string, req ConsoleRequest) (*SSHConsoleSession, error) {
	cmd := req.Cmd
	if len(cmd) == 0 {
		shell := req.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = []string{shell}
	}
	args := append([]string{}, sshArgs...)
	args = append(args, "-tt")
	args = append(args, shellQuoteJoin(cmd))
	ec := osexec.CommandContext(ctx, resolveSSHBin(), args...)
	ec.Env = os.Environ()
	for k, v := range req.Env {
		ec.Env = append(ec.Env, k+"="+v)
	}
	rows, cols := req.Rows, req.Cols
	if rows <= 0 {
		rows = 32
	}
	if cols <= 0 {
		cols = 120
	}
	ptmx, err := pty.StartWithSize(ec, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	return &SSHConsoleSession{cmd: ec, ptmx: ptmx}, nil
}

func (s *SSHConsoleSession) Stdin() io.WriteCloser { return s.ptmx }
func (s *SSHConsoleSession) Stdout() io.Reader     { return s.ptmx }
func (s *SSHConsoleSession) Stderr() io.Reader     { return nil }
func (s *SSHConsoleSession) Resize(_ context.Context, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}
func (s *SSHConsoleSession) Wait() (int, error) {
	err := s.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if exit, ok := err.(*osexec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 1, err
}
func (s *SSHConsoleSession) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.ptmx.Close()
}

var _ substrate.ConsoleSession = (*RawConsoleSession)(nil)
var _ substrate.ConsoleSession = (*SSHConsoleSession)(nil)

func shellQuoteJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
	}
	return strings.Join(quoted, " ")
}
