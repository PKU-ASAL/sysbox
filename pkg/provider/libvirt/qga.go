package libvirt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// qgaConnection executes Windows commands through the libvirt QEMU guest agent.
// It does not require guest networking or an administrator password in HCL.
type qgaConnection struct{ domain string }

func qgaCommandTimeout(command string) string {
	if command == "guest-exec-status" {
		// Status checks may carry captured process output, so give them more time
		// than control probes.
		return "30"
	}
	if command == "guest-file-read" {
		// Windows QGA can take longer to service file reads while ETW is busy.
		return "60"
	}
	return "10"
}

type qgaReply struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
}

func (c *qgaConnection) call(ctx context.Context, command string, args any, result any) error {
	request := map[string]any{"execute": command}
	if args != nil {
		request["arguments"] = args
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	// virsh receives JSON as one argv entry. Stay below Linux's per-argument
	// limit and reject locally instead of failing ambiguously after admission.
	if len(encoded) > 64<<10 {
		return fmt.Errorf("QGA request exceeds 64 KiB transport envelope")
	}
	// The operation's parent context remains the overall bound; control probes
	// keep 10 seconds while status and file reads get longer budgets.
	timeout := qgaCommandTimeout(command)
	output, err := exec.CommandContext(ctx, "virsh", "qemu-agent-command", c.domain, "--timeout", timeout, string(encoded)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("libvirt qga %s: %w: %s", command, err, output)
	}
	var reply qgaReply
	if err := json.Unmarshal(output, &reply); err != nil {
		return fmt.Errorf("libvirt qga %s: decode response: %w", command, err)
	}
	if reply.Error != nil {
		return fmt.Errorf("libvirt qga %s: %s: %s", command, reply.Error.Class, reply.Error.Desc)
	}
	if result != nil {
		if err := json.Unmarshal(reply.Return, result); err != nil {
			return fmt.Errorf("libvirt qga %s: decode result: %w", command, err)
		}
	}
	return nil
}

func (c *qgaConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := c.call(probeCtx, "guest-ping", nil, nil)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("libvirt qga not ready on %s after %s: %w", c.domain, timeout, err)
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *qgaConnection) start(ctx context.Context, req substrate.ExecRequest, capture bool) (int, error) {
	argv, err := transport.CommandArgv(req)
	if err != nil {
		return 0, err
	}
	if req.WorkingDir != "" {
		return 0, fmt.Errorf("libvirt qga: working_dir is unsupported")
	}
	path := argv[0]
	if strings.EqualFold(path, "powershell.exe") || strings.EqualFold(path, "powershell") {
		path = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	}
	args := map[string]any{"path": path, "arg": argv[1:], "capture-output": capture}
	if len(req.Environment) > 0 {
		keys := make([]string, 0, len(req.Environment))
		for key := range req.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		env := make([]string, 0, len(keys))
		for _, key := range keys {
			env = append(env, key+"="+req.Environment[key])
		}
		if _, ok := req.Environment["SystemRoot"]; !ok {
			env = append(env, `SystemRoot=C:\Windows`)
		}
		if _, ok := req.Environment["windir"]; !ok {
			env = append(env, `windir=C:\Windows`)
		}
		if _, ok := req.Environment["SystemDrive"]; !ok {
			env = append(env, `SystemDrive=C:`)
		}
		if _, ok := req.Environment["ComSpec"]; !ok {
			env = append(env, `ComSpec=C:\Windows\System32\cmd.exe`)
		}
		if _, ok := req.Environment["PATHEXT"]; !ok {
			env = append(env, `PATHEXT=.COM;.EXE;.BAT;.CMD;.VBS;.JS;.WS;.MSC`)
		}
		if _, ok := req.Environment["PATH"]; !ok {
			env = append(env, `PATH=C:\Windows\System32;C:\Windows;C:\Windows\System32\WindowsPowerShell\v1.0`)
		}
		args["env"] = env
	}
	if req.Stdin != nil {
		input, err := io.ReadAll(io.LimitReader(req.Stdin, 16<<20))
		if err != nil {
			return 0, err
		}
		args["input-data"] = base64.StdEncoding.EncodeToString(input)
	}
	var started struct {
		PID int `json:"pid"`
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := c.call(ctx, "guest-exec", args, &started)
		if err == nil {
			break
		}
		if !transientQGAError(err) || time.Now().After(deadline) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if started.PID <= 0 {
		return 0, fmt.Errorf("libvirt qga: guest-exec returned invalid PID")
	}
	return started.PID, nil
}

func (c *qgaConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	return c.start(ctx, req, false)
}

func (c *qgaConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	out := substrate.NewExecCapture(req.OutputLimitBytes, stdout)
	errOut := substrate.NewExecCapture(req.OutputLimitBytes, stderr)
	defer func() {
		result.Stdout, result.Stderr = out.String(), errOut.String()
		result.StdoutTruncated = result.StdoutTruncated || out.Truncated()
		result.StderrTruncated = result.StderrTruncated || errOut.Truncated()
		result, err = substrate.FinishExecution(ctx, result, err)
		if err == nil && result.ExitCodeKnown {
			result.RemoteTermination = substrate.RemoteTerminationConfirmed
		}
	}()
	stdout, stderr = out, errOut

	if req.CaptureViaFile {
		return c.execCapturedFile(ctx, req, stdout, stderr)
	}
	return c.execWithCapture(ctx, req, stdout, stderr, true)
}

func (c *qgaConnection) execWithCapture(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer, capture bool) (substrate.ExecResult, error) {
	pid, err := c.start(ctx, req, capture)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	for {
		var status struct {
			Exited       bool   `json:"exited"`
			ExitCode     int    `json:"exitcode"`
			OutData      string `json:"out-data"`
			ErrData      string `json:"err-data"`
			OutTruncated bool   `json:"out-truncated"`
			ErrTruncated bool   `json:"err-truncated"`
		}
		if err := c.call(ctx, "guest-exec-status", map[string]any{"pid": pid}, &status); err != nil {
			if !transientQGAError(err) {
				return substrate.ExecResult{}, err
			}
			select {
			case <-ctx.Done():
				return substrate.ExecResult{}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if status.Exited {
			out, err := base64.StdEncoding.DecodeString(status.OutData)
			if err != nil {
				return substrate.ExecResult{}, err
			}
			errOut, err := base64.StdEncoding.DecodeString(status.ErrData)
			if err != nil {
				return substrate.ExecResult{}, err
			}
			result := substrate.ExecResult{Stdout: string(out), Stderr: string(errOut), ExitCode: status.ExitCode, ExitCodeKnown: true, Status: substrate.ExecStatusExited, RemoteTermination: substrate.RemoteTerminationConfirmed}
			if stdout != nil {
				_, _ = stdout.Write(out)
			}
			if stderr != nil {
				_, _ = stderr.Write(errOut)
			}
			if status.OutTruncated || status.ErrTruncated {
				return result, fmt.Errorf("libvirt qga: guest output was truncated")
			}
			return result, nil
		}
		select {
		case <-ctx.Done():
			return substrate.ExecResult{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *qgaConnection) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer file.Close()
	var handle int
	if err := c.call(ctx, "guest-file-open", map[string]any{"path": dstPath, "mode": "wb"}, &handle); err != nil {
		return err
	}
	defer func() { _ = c.call(context.Background(), "guest-file-close", map[string]any{"handle": handle}, nil) }()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			var written struct {
				Count int `json:"count"`
			}
			if err := c.call(ctx, "guest-file-write", map[string]any{"handle": handle, "buf-b64": base64.StdEncoding.EncodeToString(buffer[:n])}, &written); err != nil {
				return err
			}
			if written.Count != n {
				return fmt.Errorf("libvirt qga: short guest file write: %d of %d", written.Count, n)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

var _ substrate.Connection = (*qgaConnection)(nil)
var _ substrate.ConnectionWaiter = (*qgaConnection)(nil)

func transientQGAError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "guest agent not available") ||
		strings.Contains(message, "guest agent is not connected")
}
