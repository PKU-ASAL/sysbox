package controlplane

import (
	"encoding/base64"

	"github.com/oslab/sysbox/pkg/substrate"
)

// GuestTaskResult is host evidence, independent of a worker's delivery lease.
type GuestTaskResult struct {
	Result        GuestExecutionResult `json:"result"`
	ExitCodeKnown bool                 `json:"exit_code_known"`
	ResultClass   string               `json:"result_class"`
}

func NewGuestTaskResult(r substrate.ExecResult) (GuestTaskResult, error) {
	if r.RemoteTermination != substrate.RemoteTerminationConfirmed || r.Status == substrate.ExecStatusRunning {
		return GuestTaskResult{}, ErrInvalidExecutionGrant
	}
	out := GuestTaskResult{ExitCodeKnown: r.ExitCodeKnown, ResultClass: "unknown"}
	if r.ExitCodeKnown {
		out.ResultClass = GuestExecutionResultClassExit
		out.Result.ExitCode = r.ExitCode
	}
	if !r.ExitCodeKnown && r.ErrorClass == substrate.ExecErrorCanceled {
		out.ResultClass = "cancelled"
	}
	out.Result.Encoding = "base64"
	out.Result.Truncated = r.StdoutTruncated || r.StderrTruncated
	encode := func(s string) string {
		if len(s) > substrate.DefaultExecOutputLimit {
			s = s[:substrate.DefaultExecOutputLimit]
			out.Result.Truncated = true
		}
		return base64.StdEncoding.EncodeToString([]byte(s))
	}
	out.Result.Stdout, out.Result.Stderr = encode(r.Stdout), encode(r.Stderr)
	return out, nil
}

func (r GuestTaskResult) Validate() error {
	if r.ExitCodeKnown {
		if r.ResultClass != GuestExecutionResultClassExit {
			return ErrInvalidExecutionGrant
		}
	} else if (r.ResultClass != "unknown" && r.ResultClass != "cancelled") || r.Result.ExitCode != 0 {
		return ErrInvalidExecutionGrant
	}
	if r.Result.Encoding != "base64" {
		return ErrInvalidExecutionGrant
	}
	for _, s := range []string{r.Result.Stdout, r.Result.Stderr} {
		if len(s) > base64.StdEncoding.EncodedLen(substrate.DefaultExecOutputLimit) {
			return ErrInvalidExecutionGrant
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(s)
		if err != nil || len(raw) > substrate.DefaultExecOutputLimit {
			return ErrInvalidExecutionGrant
		}
	}
	return nil
}
