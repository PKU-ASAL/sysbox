package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"
	"github.com/oslab/sysbox/pkg/substrate"
)

// WinRMConnection is a foreground-only transport. Durable task execution is
// deliberately not implemented by WS-Man; provider connections retain the
// existing signed Windows helper for that separate capability.
type WinRMConnection struct {
	endpoint       *url.URL
	user, password string
	run            func(context.Context, string, io.Writer, io.Writer) (int, error)
}

func NewWinRMConnection(endpoint, user, password string) (*WinRMConnection, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/wsman" || user == "" || password == "" {
		return nil, fmt.Errorf("WinRM requires an HTTPS /wsman endpoint and resolved credentials")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid WinRM port")
		}
	}
	return &WinRMConnection{endpoint: u, user: user, password: password}, nil
}

// authenticatedSOAP supplies request context, redirect refusal, response bounds
// and TLS verification around the library's WS-Man parser and shell protocol.
// The library's default HTTP client does not carry the operation context.
type authenticatedSOAP struct {
	ctx                      context.Context
	endpoint, user, password string
	client                   *http.Client
}

func (c *authenticatedSOAP) Transport(*winrm.Endpoint) error { return nil }
func (c *authenticatedSOAP) Post(_ *winrm.Client, message *soap.SoapMessage) (string, error) {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.endpoint, strings.NewReader(message.String()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	req.SetBasicAuth(c.user, c.password)
	response, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusInternalServerError {
		return "", fmt.Errorf("WinRM HTTP status %d", response.StatusCode)
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/soap+xml") {
		return "", fmt.Errorf("WinRM response content type invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 2<<20 {
		return "", fmt.Errorf("WinRM SOAP response exceeds limit")
	}
	return string(raw), nil
}
func (c *WinRMConnection) runPowerShell(ctx context.Context, script string, out, errOut io.Writer) (int, error) {
	port := 443
	if c.endpoint.Port() != "" {
		port, _ = strconv.Atoi(c.endpoint.Port())
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	defer tr.CloseIdleConnections()
	httpClient := &http.Client{Transport: tr, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	wire := &authenticatedSOAP{ctx: ctx, endpoint: c.endpoint.String(), user: c.user, password: c.password, client: httpClient}
	params := winrm.NewParameters("PT20S", "en-US", 153600)
	params.TransportDecorator = func() winrm.Transporter { return wire }
	client, err := winrm.NewClientWithParameters(winrm.NewEndpoint(c.endpoint.Hostname(), port, true, false, nil, nil, nil, 30*time.Second), c.user, c.password, params)
	if err != nil {
		return 0, err
	}
	return client.RunWithContext(ctx, winrm.Powershell(script), out, errOut)
}
func (c *WinRMConnection) Exec(ctx context.Context, req substrate.ExecRequest, out, errOut io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	// Give foreground requests a bounded default even when callers omit one.
	ctx, bound := context.WithTimeout(ctx, 5*time.Minute)
	defer bound()
	defer func() { result, err = substrate.FinishExecution(ctx, result, err) }()
	if err = req.Validate(); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if (req.Shell != substrate.ShellPowerShell && req.Shell != substrate.ShellNone) || req.Stdin != nil || req.CaptureViaFile || req.Terminal != nil {
		return result, fmt.Errorf("WinRM foreground supports PowerShell or direct argv without stdin or file capture")
	}
	script, err := winRMScript(req)
	if err != nil {
		return result, err
	}
	stdout := substrate.NewExecCapture(req.OutputLimitBytes, out)
	stderr := substrate.NewExecCapture(req.OutputLimitBytes, errOut)
	runner := c.run
	if runner == nil {
		runner = c.runPowerShell
	}
	code, runErr := runner(ctx, script, stdout, stderr)
	result = substrate.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated(), ExitCode: code, ExitCodeKnown: runErr == nil, RemoteTermination: substrate.RemoteTerminationUnknown}
	if runErr != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("WinRM foreground outcome unknown; no automatic retry")
	}
	return result, nil
}
func winRMScript(req substrate.ExecRequest) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	var script strings.Builder
	script.WriteString("$ErrorActionPreference='Stop';\n")
	keys := make([]string, 0, len(req.Environment))
	for key := range req.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&script, "[Environment]::SetEnvironmentVariable(%s,%s,'Process');\n", quote(key), quote(req.Environment[key]))
	}
	if req.WorkingDir != "" {
		fmt.Fprintf(&script, "Set-Location -LiteralPath %s;\n", quote(req.WorkingDir))
	}
	if req.Shell == substrate.ShellPowerShell {
		fmt.Fprintf(&script, "$s=[ScriptBlock]::Create(%s); $a=@(", quote(req.Program))
	} else {
		fmt.Fprintf(&script, "$s=%s; $a=@(", quote(req.Program))
	}
	for i, arg := range req.Args {
		if i > 0 {
			script.WriteByte(',')
		}
		script.WriteString(quote(arg))
	}
	script.WriteString("); & $s @a; if(-not $?){if($LASTEXITCODE){exit $LASTEXITCODE};exit 1}\n")
	if len(script.String()) > 32<<10 {
		return "", fmt.Errorf("WinRM command exceeds 32 KiB limit")
	}
	return script.String(), nil
}
func (c *WinRMConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := c.Exec(ctx, substrate.ExecRequest{Program: "exit 0", Shell: substrate.ShellPowerShell}, nil, nil)
	if err != nil {
		return err
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 {
		return fmt.Errorf("WinRM readiness failed")
	}
	return nil
}
