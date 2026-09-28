package substrate

import (
	"bytes"
	"context"
	"errors"
	"io"
)

const DefaultExecOutputLimit = 1 << 20

type ExecStatus string

const (
	ExecStatusExited  ExecStatus = "exited"
	ExecStatusUnknown ExecStatus = "unknown"
)

type ExecErrorClass string

const (
	ExecErrorInvalid   ExecErrorClass = "invalid"
	ExecErrorTransport ExecErrorClass = "transport"
	ExecErrorTimeout   ExecErrorClass = "timeout"
	ExecErrorCanceled  ExecErrorClass = "cancelled"
)

type RemoteTermination string

const (
	RemoteTerminationUnknown   RemoteTermination = "unknown"
	RemoteTerminationConfirmed RemoteTermination = "confirmed"
)

// ExecutionHandle identifies an execution independently of its diagnostic PID.
// An ID alone does not promise cross-restart observation or cancellation.
type ExecutionHandle struct {
	ID         string
	NodeID     string
	Generation int64
	PID        int
}

type ConnectionStarter interface {
	Start(context.Context, ExecRequest) (ExecutionHandle, error)
}
type ConnectionExecutionObserver interface {
	ObserveExecution(context.Context, ExecutionHandle) (ExecResult, error)
}
type ConnectionExecutionCanceler interface {
	CancelExecution(context.Context, ExecutionHandle) (RemoteTermination, error)
}
type ConnectionFileCopier interface {
	CopyFile(context.Context, string, string) error
}

// ConnectionBackground is the transitional PID-only adapter. Prefer Start.
type ConnectionBackground interface {
	ExecBackground(context.Context, ExecRequest) (int, error)
}

// ExecCapture caps retained and forwarded output, but consumes every byte so
// transports can drain their protocol after the limit without blocking guests.
// Each stream owns a capture and writes to it from one goroutine.
type ExecCapture struct {
	buffer    bytes.Buffer
	limit     int
	writer    io.Writer
	truncated bool
}

func NewExecCapture(limit int, writer io.Writer) *ExecCapture {
	if limit <= 0 {
		limit = DefaultExecOutputLimit
	}
	return &ExecCapture{limit: limit, writer: writer}
}
func (c *ExecCapture) Write(p []byte) (int, error) {
	size := len(p)
	room := c.limit - c.buffer.Len()
	if len(p) > room {
		p = p[:room]
		c.truncated = true
	}
	_, _ = c.buffer.Write(p)
	if len(p) > 0 && c.writer != nil {
		n, err := c.writer.Write(p)
		if err != nil {
			return n, err
		}
		if n != len(p) {
			return n, io.ErrShortWrite
		}
	}
	return size, nil
}
func (c *ExecCapture) String() string  { return c.buffer.String() }
func (c *ExecCapture) Truncated() bool { return c.truncated }

// FinishExecution classifies local transport completion without asserting that
// closing a socket or killing an SSH client terminated the remote process.
func FinishExecution(ctx context.Context, result ExecResult, err error) (ExecResult, error) {
	result.RemoteTermination = RemoteTerminationUnknown
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		result.Status = ExecStatusUnknown
		result.ExitCodeKnown = false
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			result.ErrorClass = ExecErrorTimeout
		case errors.Is(err, context.Canceled):
			result.ErrorClass = ExecErrorCanceled
		default:
			if result.ErrorClass == "" {
				result.ErrorClass = ExecErrorTransport
			}
		}
		return result, err
	}
	if result.ExitCodeKnown {
		result.Status = ExecStatusExited
	} else {
		result.Status = ExecStatusUnknown
	}
	return result, nil
}
