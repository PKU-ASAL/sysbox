package agentexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

func runGrantedConsole(ctx context.Context, opts Options, cmd controlplane.AgentCommand, ws *websocket.Conn) error {
	task, err := startGrantedTask(ctx, opts, cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = task.client.CancelExecution(cleanup, task.handle)
	}()
	return relayGrantedConsole(ctx, opts, cmd, task, ws)
}

func relayGrantedConsole(ctx context.Context, opts Options, cmd controlplane.AgentCommand, task *grantedTask, ws *websocket.Conn) (relayErr error) {
	ctx, cancel := context.WithCancel(ctx)
	input := make(chan consoleFrame)
	inputErr := make(chan error, 1)
	readerDone := make(chan struct{})
	ws.SetReadLimit(16 << 10)
	go func() {
		defer close(readerDone)
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				inputErr <- err
				return
			}
			var frame consoleFrame
			if err = json.Unmarshal(raw, &frame); err != nil {
				inputErr <- err
				return
			}
			select {
			case input <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		if relayErr != nil && ctx.Err() == nil {
			_ = writeConsoleFrame(ctx, ws, consoleFrame{Type: "error", Error: relayErr.Error()})
		}
		cancel()
		<-readerDone
	}()
	cursors := map[string]int64{"stdout": 0, "stderr": 0}
	flush := func() error {
		for _, stream := range []string{"stdout", "stderr"} {
			for {
				cursor := cursors[stream]
				page, err := task.client.ReadExecutionOutput(ctx, task.handle, stream, cursor, 8192)
				if err != nil {
					return err
				}
				if len(page.Data) > 8192 || page.NextCursor != cursor+int64(len(page.Data)) {
					return fmt.Errorf("invalid console output cursor")
				}
				if len(page.Data) > 0 {
					if err = writeConsoleFrame(ctx, ws, consoleFrame{Type: stream, Data: base64.StdEncoding.EncodeToString(page.Data)}); err != nil {
						return err
					}
				}
				cursors[stream] = page.NextCursor
				if len(page.Data) < 8192 {
					break
				}
			}
		}
		return nil
	}
	budget, err := controlplane.ExecutionGrantBudget(task.grant, time.Now())
	if err != nil {
		return err
	}
	renewAt := time.Now().Add(budget / 3)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := task.client.ObserveExecution(ctx, task.handle)
		if err != nil {
			return err
		}
		if err = flush(); err != nil {
			return err
		}
		if result.RemoteTermination == substrate.RemoteTerminationConfirmed {
			if result.StdoutTruncated || result.StderrTruncated {
				return fmt.Errorf("console output limit exceeded")
			}
			if !result.ExitCodeKnown {
				return guesttask.ErrUnknown
			}
			return writeConsoleFrame(ctx, ws, consoleFrame{Type: "exit", Code: result.ExitCode})
		}
		if !time.Now().Before(renewAt) {
			var renewed controlplane.SignedExecutionGrant
			if err = post(ctx, opts, task.endpoint+"/renew", map[string]any{"grant": task.grant, "lease_owner": cmd.LeaseOwner}, &renewed); err != nil {
				return err
			}
			if err = task.check(renewed); err != nil {
				return err
			}
			if renewed.Grant.Epoch != task.grant.Grant.Epoch {
				return guesttask.ErrConflict
			}
			if err = task.client.RenewExecution(transport.WithTaskGrant(ctx, task.handle.ID, renewed), task.handle); err != nil {
				return err
			}
			task.grant = renewed
			budget, err = controlplane.ExecutionGrantBudget(renewed, time.Now())
			if err != nil {
				return err
			}
			renewAt = time.Now().Add(budget / 3)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputErr:
			return err
		case frame := <-input:
			if frame.Type == "close" {
				return errors.New("console client requested closure; awaiting host stop")
			}
			control := substrate.TerminalControl{Type: frame.Type, Cols: frame.Cols, Rows: frame.Rows}
			if frame.Type == "stdin" {
				control.Data, err = base64.StdEncoding.DecodeString(frame.Data)
				if err != nil {
					return fmt.Errorf("console stdin must be base64")
				}
			}
			if err = control.Validate(); err != nil {
				return err
			}
			if err = task.client.ControlExecution(transport.WithTaskGrant(ctx, task.handle.ID, task.grant), task.handle, control); err != nil {
				return err
			}
		case <-ticker.C:
		}
	}
}
