package transport

import (
	"context"
	"errors"
	"github.com/masterzen/winrm/soap"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWinRMRequiresHTTPSAndNoURLCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://192.0.2.1:5985/wsman", "https://u:p@192.0.2.1:5986/wsman", "https://192.0.2.1/other", "https://192.0.2.1/wsman?x=y"} {
		_, err := NewWinRMConnection(endpoint, "user", "secret")
		require.Error(t, err)
	}
	c, err := NewWinRMConnection("https://192.0.2.1:5986/wsman", "user", "secret")
	require.NoError(t, err)
	require.NotNil(t, c)
}
func TestWinRMUnknownEffectNeverRetries(t *testing.T) {
	c, err := NewWinRMConnection("https://192.0.2.1:5986/wsman", "user", "secret")
	require.NoError(t, err)
	calls := 0
	c.run = func(context.Context, string, io.Writer, io.Writer) (int, error) {
		calls++
		return 0, errors.New("ambiguous fault containing secret")
	}
	result, err := c.Exec(context.Background(), substrate.ExecRequest{Program: "Write-Output ok", Shell: substrate.ShellPowerShell}, nil, nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
	require.Equal(t, 1, calls)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, substrate.ExecStatusUnknown, result.Status)
	require.NotEqual(t, substrate.RemoteTerminationConfirmed, result.RemoteTermination)
}
func TestWinRMPowerShellOutputAndCancellation(t *testing.T) {
	c, err := NewWinRMConnection("https://192.0.2.1:5986/wsman", "user", "secret")
	require.NoError(t, err)
	c.run = func(ctx context.Context, script string, out, stderr io.Writer) (int, error) {
		_, _ = io.WriteString(out, "hello")
		return 0, nil
	}
	result, err := c.Exec(context.Background(), substrate.ExecRequest{Program: "Write-Output hello", Shell: substrate.ShellPowerShell, OutputLimitBytes: 3}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "hel", result.Stdout)
	require.True(t, result.StdoutTruncated)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Exec(ctx, substrate.ExecRequest{Program: "exit 0", Shell: substrate.ShellPowerShell}, nil, nil)
	require.Error(t, err)
}

func TestWinRMRejectsUntrustedTLSWithoutSendingCredentials(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	c, err := NewWinRMConnection(server.URL+"/wsman", "operator", "credential-canary")
	require.NoError(t, err)
	result, err := c.Exec(context.Background(), substrate.ExecRequest{Program: "exit 0", Shell: substrate.ShellPowerShell, Timeout: time.Second}, nil, nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "credential-canary")
	require.Zero(t, requests.Load())
	require.False(t, result.ExitCodeKnown)
}
func TestAuthenticatedSOAPUsesBoundContextAndRefusesRedirect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "https://invalid.example/wsman", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	wire := &authenticatedSOAP{ctx: context.Background(), endpoint: server.URL, user: "operator", password: "secret", client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	message := soap.NewMessage()
	_, err := wire.Post(nil, message)
	require.Error(t, err)
	require.EqualValues(t, 1, requests.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wire.ctx = ctx
	_, err = wire.Post(nil, message)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, requests.Load())
}
func TestWinRMDirectArgvPreservesQuotedValues(t *testing.T) {
	request := substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-Command", "Write-Output 'hello'"}, Shell: substrate.ShellNone, Environment: map[string]string{"NAME": "a'b"}}
	script, err := winRMScript(request)
	require.NoError(t, err)
	require.Contains(t, script, "$s='powershell.exe'")
	require.Contains(t, script, "'Write-Output ''hello'''")
}

func TestAuthenticatedSOAPUsesCredentialsOnlyOnVerifiedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "operator", user)
		require.Equal(t, "secret", password)
		require.Equal(t, "/wsman", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = io.WriteString(w, "<Envelope/>")
	}))
	defer server.Close()
	wire := &authenticatedSOAP{ctx: context.Background(), endpoint: server.URL + "/wsman", user: "operator", password: "secret", client: server.Client()}
	raw, err := wire.Post(nil, soap.NewMessage())
	require.NoError(t, err)
	require.Equal(t, "<Envelope/>", raw)
}
