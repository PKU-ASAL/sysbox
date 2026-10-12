package libvirt

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Explicit file capture avoids large guest-exec-status replies. Each QGA file
// read remains small; the caller still receives bounded, ordinary exec output.
func (c *qgaConnection) execCapturedFile(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (substrate.ExecResult, error) {
	if req.Shell != substrate.ShellNone || req.WorkingDir != "" || req.Stdin != nil {
		return substrate.ExecResult{}, fmt.Errorf("libvirt qga: file capture requires direct argv without working_dir or stdin")
	}
	args := append([]string{req.Program}, req.Args...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		q, err := windowsBatchArg(arg)
		if err != nil {
			return substrate.ExecResult{}, err
		}
		quoted[i] = q
	}
	if len(strings.Join(quoted, " ")) > 8000 {
		return substrate.ExecResult{}, fmt.Errorf("libvirt qga: file capture command is too long")
	}
	base := `C:\Windows\Temp\sysbox-exec-` + uuid.NewString()
	scriptPath, outPath, errPath := base+".cmd", base+".stdout", base+".stderr"
	script := "@echo off\r\nchcp 65001 >nul\r\nsetlocal DisableDelayedExpansion\r\n" + strings.Join(quoted, " ") + ` 1>"` + outPath + `" 2>"` + errPath + "\"\r\nexit /b %ERRORLEVEL%\r\n"
	local, err := os.CreateTemp("", "sysbox-qga-capture-*.cmd")
	if err != nil {
		return substrate.ExecResult{}, err
	}
	defer os.Remove(local.Name())
	if _, err = local.WriteString(script); err != nil {
		local.Close()
		return substrate.ExecResult{}, err
	}
	local.Close()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.execWithCapture(cleanup, substrate.ExecRequest{Program: `C:\Windows\System32\cmd.exe`, Args: []string{"/D", "/V:OFF", "/C", `del /q "` + scriptPath + `" "` + outPath + `" "` + errPath + `"`}, Shell: substrate.ShellNone}, nil, nil, true)
	}()
	if err = c.CopyFile(ctx, local.Name(), scriptPath); err != nil {
		return substrate.ExecResult{}, err
	}
	run := req
	run.Program = `C:\Windows\System32\cmd.exe`
	run.Args = []string{"/D", "/V:OFF", "/C", scriptPath}
	run.CaptureViaFile = false
	result, err := c.execWithCapture(ctx, run, nil, nil, false)
	if err != nil {
		return result, err
	}
	out, err := c.readCapturedFile(ctx, outPath, 4<<20+1)
	if err != nil {
		return result, err
	}
	errOut, err := c.readCapturedFile(ctx, errPath, 4<<20+1)
	if err != nil {
		return result, err
	}
	result.Stdout, result.Stderr = string(out), string(errOut)
	if stdout != nil {
		_, _ = stdout.Write(out)
	}
	if stderr != nil {
		_, _ = stderr.Write(errOut)
	}
	return result, nil
}

func windowsBatchArg(arg string) (string, error) {
	// This opt-in mode rejects characters expanded by a batch parser. Default
	// direct execution remains available for arbitrary native argument vectors.
	if strings.ContainsAny(arg, "\"%\r\n") {
		return "", fmt.Errorf("libvirt qga: file capture argument contains batch expansion characters")
	}
	trailing := len(arg) - len(strings.TrimRight(arg, `\`))
	return `"` + arg + strings.Repeat(`\`, trailing) + `"`, nil
}

func (c *qgaConnection) readCapturedFile(ctx context.Context, path string, limit int) ([]byte, error) {
	var handle int
	if err := c.call(ctx, "guest-file-open", map[string]any{"path": path, "mode": "rb"}, &handle); err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.call(closeCtx, "guest-file-close", map[string]any{"handle": handle}, nil)
	}()
	var output bytes.Buffer
	for output.Len() < limit {
		count := min(1<<20, limit-output.Len())
		var chunk struct {
			Count int    `json:"count"`
			Data  string `json:"buf-b64"`
			EOF   bool   `json:"eof"`
		}
		if err := c.call(ctx, "guest-file-read", map[string]any{"handle": handle, "count": count}, &chunk); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || len(data) != chunk.Count || len(data) > count {
			return nil, fmt.Errorf("libvirt qga: invalid captured file chunk")
		}
		output.Write(data)
		if chunk.EOF || len(data) == 0 {
			break
		}
	}
	return output.Bytes(), nil
}
