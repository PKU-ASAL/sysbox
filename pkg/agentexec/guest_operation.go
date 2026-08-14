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

func executeGuestOperation(ctx context.Context, st *state.State, node string, req controlplane.GuestExecutionRequest) guestOperationCompletion {
	res := st.FindResource(address.Resource("sysbox_node", node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", node))
	}
	if res == nil {
		return guestOperationCompletion{ResultClass: "not_found", Err: "logical node not found"}
	}
	exec, err := driver.DefaultRegistry.RequireGuestExec(res.Driver)
	if err != nil {
		return guestOperationCompletion{ResultClass: "unsupported", Err: "guest execution capability unavailable"}
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return guestOperationCompletion{ResultClass: "unsupported", Err: "node state capability unavailable"}
	}
	handle, err := res.ReconstructHandle(codec)
	if err != nil {
		return guestOperationCompletion{ResultClass: "provider", Err: "reconstruct provider handle failed"}
	}
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	result, err := exec.ExecInNode(ctx, handle, substrate.ExecRequest{Program: req.Argv[0], Args: req.Argv[1:], Environment: req.Environment, WorkingDir: req.WorkingDirectory, Shell: substrate.ShellNone})
	if err != nil {
		class := "provider"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "timeout"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			class = "cancelled"
		}
		return guestOperationCompletion{ResultClass: class, Err: class + " guest operation"}
	}
	stdout, outTruncated := bounded([]byte(result.Stdout), guestOutputLimit)
	stderr, errTruncated := bounded([]byte(result.Stderr), guestOutputLimit)
	return guestOperationCompletion{ResultClass: controlplane.GuestExecutionResultClassExit, Result: controlplane.GuestExecutionResult{
		ExitCode: result.ExitCode, Stdout: base64.StdEncoding.EncodeToString(stdout), Stderr: base64.StdEncoding.EncodeToString(stderr), Encoding: "base64", Truncated: outTruncated || errTruncated,
	}}
}

func bounded(data []byte, limit int) ([]byte, bool) {
	if len(data) <= limit {
		return data, false
	}
	return data[:limit], true
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
		return fmt.Errorf("copy guest payload failed")
	}
	return nil
}

const guestFileMaxAgentSize = 16 << 20
