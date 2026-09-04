package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

const guestOutputLimit = 1024 * 1024

type guestOperationCompletion struct {
	Result      controlplane.GuestExecutionResult
	ResultClass string
	Err         string
}

var (
	errGuestNodeNotFound    = errors.New("guest node not found")
	errGuestExecUnsupported = errors.New("guest exec capability unavailable")
)

// runGuestExec runs argv in a guest node identified by logical name, via the
// guest-exec capability. It is the single "run a command in a node" primitive,
// shared by the guest-execution API (executeGuestOperation) and check evaluation
// (evaluateChecks) — do not add another exec path.
//
// ctx carries the caller's timeout/cancellation; the caller applies the budget,
// so this stays a pure lookup-and-exec. It returns the raw result; the error is
// wrapped so callers can distinguish resolution failures (node not found,
// capability unavailable) from a failed exec.
func runGuestExec(ctx context.Context, st *state.State, node string, argv []string, env map[string]string, workingDir string) (substrate.ExecResult, error) {
	res := st.FindResource(address.Resource("sysbox_node", node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", node))
	}
	if res == nil {
		return substrate.ExecResult{}, fmt.Errorf("%w: %s", errGuestNodeNotFound, node)
	}
	exec, err := driver.DefaultRegistry.RequireGuestExec(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", errGuestExecUnsupported)
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", errGuestExecUnsupported)
	}
	handle, err := res.ReconstructHandle(codec)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("reconstruct provider handle: %w", err)
	}
	return exec.ExecInNode(ctx, handle, substrate.ExecRequest{Program: argv[0], Args: argv[1:], Environment: env, WorkingDir: workingDir, Shell: substrate.ShellNone})
}

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
func guestOperationContext(ctx context.Context, req controlplane.GuestExecutionRequest) (context.Context, context.CancelFunc) {
	if req.TimeoutSeconds > 0 {
		return context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	return context.WithCancel(ctx)
}

func executeGuestOperation(ctx context.Context, st *state.State, node string, req controlplane.GuestExecutionRequest) guestOperationCompletion {
	ctx, cancel := guestOperationContext(ctx, req)
	defer cancel()

	result, err := runGuestExec(ctx, st, node, req.Argv, req.Environment, req.WorkingDirectory)
	if err != nil {
		class := classifyGuestExecError(ctx, err)
		switch class {
		case "not_found":
			return guestOperationCompletion{ResultClass: class, Err: "logical node not found"}
		case "unsupported":
			return guestOperationCompletion{ResultClass: class, Err: "guest execution capability unavailable"}
		default:
			// Do not drop what the provider already captured: on a timeout that is
			// the only trace of how far the command got, and the caller has nothing
			// else to act on.
			return guestOperationCompletion{ResultClass: class, Err: class + " guest operation", Result: encodeGuestOutput(result.Stdout, result.Stderr)}
		}
	}

	out := encodeGuestOutput(result.Stdout, result.Stderr)
	out.ExitCode = result.ExitCode
	return guestOperationCompletion{ResultClass: controlplane.GuestExecutionResultClassExit, Result: out}
}

// encodeGuestOutput bounds and base64-encodes captured guest output. The bound
// applies here, at the boundary, because the provider may hand back more than
// the agent is willing to carry.
func encodeGuestOutput(stdout, stderr string) controlplane.GuestExecutionResult {
	out, outTruncated := boundedString(stdout)
	errOut, errTruncated := boundedString(stderr)
	return controlplane.GuestExecutionResult{
		Stdout:    base64.StdEncoding.EncodeToString([]byte(out)),
		Stderr:    base64.StdEncoding.EncodeToString([]byte(errOut)),
		Encoding:  "base64",
		Truncated: outTruncated || errTruncated,
	}
}

// classifyGuestExecError maps a runGuestExec error to a result class. A timeout
// is recognised by the context's deadline, not by the returned error, because a
// provider may surface a mid-stream close as a plain read failure.
func classifyGuestExecError(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, errGuestNodeNotFound):
		return "not_found"
	case errors.Is(err, errGuestExecUnsupported):
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
	if len(s) > guestOutputLimit {
		return s[:guestOutputLimit], true
	}
	return s, false
}

func putGuestFile(ctx context.Context, opts Options, st *state.State, put controlplane.GuestFilePut) error {
	res := st.FindResource(address.Resource("sysbox_node", put.Node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", put.Node))
	}
	if res == nil {
		return fmt.Errorf("logical node not found")
	}
	files, err := driver.DefaultRegistry.RequireGuestFiles(res.Driver)
	if err != nil {
		return fmt.Errorf("guest files capability unavailable")
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return fmt.Errorf("node state capability unavailable")
	}
	handle, err := res.ReconstructHandle(codec)
	if err != nil {
		return fmt.Errorf("reconstruct provider handle failed")
	}
	tmp, err := os.CreateTemp("", "sysbox-guest-file-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0600)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.APIURL+put.FetchRef, nil)
	if err != nil {
		return err
	}
	authorize(req, opts)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch guest payload: %s", resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, guestFileMaxAgentSize+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("fetch guest payload")
	}
	if n > guestFileMaxAgentSize || n != put.Size {
		return fmt.Errorf("guest payload size mismatch")
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), put.SHA256) {
		return fmt.Errorf("guest payload digest mismatch")
	}
	if err := files.CopyToNode(ctx, handle, name, put.Path, put.Mode); err != nil {
		return fmt.Errorf("copy guest payload failed: %w", err)
	}
	return nil
}

const guestFileMaxAgentSize = 16 << 20
