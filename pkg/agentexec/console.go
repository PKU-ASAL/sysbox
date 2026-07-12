package agentexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
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
	res := st.FindResource(address.Resource("sysbox_node", sess.Node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", sess.Node))
	}
	if res == nil {
		return fmt.Errorf("node %q not found in state", sess.Node)
	}
	descriptor, ok := driver.DefaultRegistry.Get(res.Driver)
	if !ok {
		return driver.Wrap(driver.ErrorNotFound, res.Driver, "driver is not registered", nil)
	}
	if descriptor.NodeState == nil {
		return driver.Wrap(driver.ErrorUnsupported, res.Driver, "node-state capability is not supported", nil)
	}
	handle, err := res.ReconstructHandle(descriptor.NodeState)
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
	if descriptor.Console != nil {
		cs, err := descriptor.Console.OpenConsole(ctx, handle, creq)
		if err != nil {
			return err
		}
		return relayConsole(ctx, cs, ws)
	}
	if descriptor.Node == nil {
		return driver.Wrap(driver.ErrorUnsupported, res.Driver, "node capability is not supported", nil)
	}
	conn, err := descriptor.Node.Connection(handle, nil)
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
	outputDone := make(chan error, 2)
	outputs := 0
	if out := cs.Stdout(); out != nil {
		outputs++
		go copyConsoleOutput(ctx, ws, "stdout", out, outputDone)
	}
	if errOut := cs.Stderr(); errOut != nil {
		outputs++
		go copyConsoleOutput(ctx, ws, "stderr", errOut, outputDone)
	}
	inputDone := make(chan error, 1)
	go copyConsoleInput(ctx, ws, cs, inputDone)
	type waitResult struct {
		code int
		err  error
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		code, err := cs.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		waitDone <- waitResult{code: code, err: err}
	}()
	for {
		select {
		case err := <-inputDone:
			return err
		case err := <-outputDone:
			outputs--
			if err != nil {
				return err
			}
		case res := <-waitDone:
			if err := drainConsoleOutput(ctx, outputDone, outputs); err != nil {
				return err
			}
			_ = writeConsoleFrame(ctx, ws, consoleFrame{Type: "exit", Code: res.code})
			return res.err
		}
	}
}

func drainConsoleOutput(ctx context.Context, outputDone <-chan error, outputs int) error {
	if outputs <= 0 {
		return nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for outputs > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-outputDone:
			outputs--
			if err != nil {
				return err
			}
		case <-timer.C:
			return nil
		}
	}
	return nil
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
