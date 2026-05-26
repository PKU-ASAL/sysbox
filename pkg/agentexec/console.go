package agentexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type consoleFrame struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

func OpenConsoleFromState(ctx context.Context, st *state.State, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, ws *websocket.Conn) error {
	res := st.FindResource("sysbox_node", sess.Node)
	if res == nil {
		res = st.FindResource("sysbox_router", sess.Node)
	}
	if res == nil {
		return fmt.Errorf("node %q not found in state", sess.Node)
	}
	sub, err := substrate.Get(res.Provider)
	if err != nil {
		return fmt.Errorf("substrate %q not registered: %w", res.Provider, err)
	}
	handle, err := res.ReconstructHandle(sub)
	if err != nil {
		return err
	}
	tty := true
	if req.TTY != nil {
		tty = *req.TTY
	}
	creq := substrate.ConsoleRequest{
		Cmd:     req.Cmd,
		Shell:   req.Shell,
		Env:     req.Env,
		WorkDir: req.WorkDir,
		TTY:     tty,
		Cols:    req.Cols,
		Rows:    req.Rows,
	}
	if cp, ok := sub.(substrate.ConsoleProvider); ok {
		cs, err := cp.OpenConsole(ctx, handle, creq)
		if err != nil {
			return err
		}
		return relayConsole(ctx, cs, ws)
	}
	conn, err := sub.Connection(handle, nil)
	if err != nil {
		return err
	}
	if conn == nil {
		return substrate.ErrNotSupported
	}
	return runConsoleFallback(ctx, conn, creq, ws)
}

func relayConsole(ctx context.Context, cs substrate.ConsoleSession, ws *websocket.Conn) error {
	defer cs.Close()
	go func() {
		<-ctx.Done()
		_ = cs.Close()
	}()
	done := make(chan error, 3)
	if out := cs.Stdout(); out != nil {
		go copyConsoleOutput(ctx, ws, "stdout", out, done)
	}
	if errOut := cs.Stderr(); errOut != nil {
		go copyConsoleOutput(ctx, ws, "stderr", errOut, done)
	}
	go copyConsoleInput(ctx, ws, cs, done)
	go func() {
		code, err := cs.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		_ = writeConsoleFrame(ctx, ws, consoleFrame{Type: "exit", Code: code})
		done <- err
	}()
	err := <-done
	return err
}

func runConsoleFallback(ctx context.Context, conn substrate.Connection, req substrate.ConsoleRequest, ws *websocket.Conn) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := conn.ExecStream(ctx, []string{consoleShell(req)}, pw, pw)
		_ = pw.Close()
		errCh <- err
	}()
	done := make(chan error, 2)
	go copyConsoleOutput(ctx, ws, "stdout", pr, done)
	go func() {
		err := <-errCh
		code := 0
		if err != nil {
			code = 1
			_ = writeConsoleFrame(ctx, ws, consoleFrame{Type: "error", Error: err.Error()})
		}
		_ = writeConsoleFrame(ctx, ws, consoleFrame{Type: "exit", Code: code})
		done <- err
	}()
	return <-done
}

func consoleShell(req substrate.ConsoleRequest) string {
	if len(req.Cmd) > 0 {
		return shellQuoteJoin(req.Cmd)
	}
	if req.Shell != "" {
		return req.Shell
	}
	return "/bin/sh"
}

func copyConsoleOutput(ctx context.Context, ws *websocket.Conn, typ string, r io.Reader, done chan<- error) {
	buf := make([]byte, 8192)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			frame := consoleFrame{Type: typ, Data: base64.StdEncoding.EncodeToString(buf[:n])}
			if werr := writeConsoleFrame(ctx, ws, frame); werr != nil {
				done <- werr
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				done <- nil
			} else {
				done <- err
			}
			return
		}
	}
}

func copyConsoleInput(ctx context.Context, ws *websocket.Conn, cs substrate.ConsoleSession, done chan<- error) {
	stdin := cs.Stdin()
	defer stdin.Close()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			done <- err
			return
		}
		var frame consoleFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			done <- err
			return
		}
		switch frame.Type {
		case "stdin":
			raw, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				raw = []byte(frame.Data)
			}
			if _, err := stdin.Write(raw); err != nil {
				done <- err
				return
			}
		case "resize":
			if err := cs.Resize(ctx, frame.Cols, frame.Rows); err != nil {
				done <- err
				return
			}
		case "close":
			done <- nil
			return
		}
	}
}

func writeConsoleFrame(ctx context.Context, ws *websocket.Conn, frame consoleFrame) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, raw)
}

func shellQuoteJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
	}
	return strings.Join(quoted, " ")
}
