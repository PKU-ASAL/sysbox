package controlplane

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

const (
	GuestOutputLimit             = 1024 * 1024
	guestExecutionMaxOutputLimit = 48 << 20
)

type GuestOperationCompletion struct {
	Result      GuestExecutionResult
	ResultClass string
	Err         string
}

var (
	ErrGuestNodeNotFound    = errors.New("guest node not found")
	ErrGuestExecUnsupported = errors.New("guest exec capability unavailable")
)

// guestOperationContext derives the context a guest operation runs under.
//
// The declared timeout is meant to bound the operation an operator asked for,
// not merely the exec call at the end of it. Reporting the execution as
// started, resolving a state manager and loading state all happen before the
// exec, and a slow or wedged state backend there burns unbounded time — so the
// budget has to be started by the caller, before any of that, rather than
// derived deep inside the exec path.
//
// A tighter parent bound always wins: context.WithTimeout keeps the earlier of
// the two deadlines, so the caller's limit stays an upper limit.
func GuestOperationContext(ctx context.Context, req GuestExecutionRequest) (context.Context, context.CancelFunc) {
	if req.TimeoutSeconds > 0 {
		return context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	return context.WithCancel(ctx)
}

func EncodeGuestResult(result substrate.ExecResult) GuestExecutionResult {
	out := encodeGuestOutput(result.Stdout, result.Stderr)
	out.Truncated = out.Truncated || result.StdoutTruncated || result.StderrTruncated
	return out
}

// encodeGuestOutput bounds and base64-encodes captured guest output. The bound
// applies here, at the boundary, because the provider may hand back more than
// the agent is willing to carry.
func encodeGuestOutput(stdout, stderr string) GuestExecutionResult {
	return EncodeGuestOutputWithLimit(stdout, stderr, 0)
}

func EncodeGuestOutputWithLimit(stdout, stderr string, limit int) GuestExecutionResult {
	if limit <= 0 {
		limit = GuestOutputLimit
	}
	if limit > guestExecutionMaxOutputLimit {
		limit = guestExecutionMaxOutputLimit
	}
	bound := func(s string) (string, bool) {
		if len(s) > limit {
			return s[:limit], true
		}
		return s, false
	}
	out, outTruncated := bound(stdout)
	errOut, errTruncated := bound(stderr)
	return GuestExecutionResult{
		Stdout:    base64.StdEncoding.EncodeToString([]byte(out)),
		Stderr:    base64.StdEncoding.EncodeToString([]byte(errOut)),
		Encoding:  "base64",
		Truncated: outTruncated || errTruncated,
	}
}

// classifyGuestExecError maps a runGuestExec error to a result class. A timeout
// is recognised by the context's deadline, not by the returned error, because a
// provider may surface a mid-stream close as a plain read failure.
func ClassifyGuestExecError(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrGuestNodeNotFound):
		return "not_found"
	case errors.Is(err, ErrGuestExecUnsupported):
		return "unsupported"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	default:
		return "provider"
	}
}

// boundedString returns s truncated to guestOutputLimit bytes plus whether it
// was truncated. It slices the string in place so a large provider output is
// never fully copied before the bound applies.
func boundedString(s string) (string, bool) {
	if len(s) > GuestOutputLimit {
		return s[:GuestOutputLimit], true
	}
	return s, false
}
