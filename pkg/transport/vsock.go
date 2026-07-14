package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

// VsockConnection implements Connection by dialling a Firecracker-style
// vsock UDS socket and speaking the protocol defined in pkg/vsockrpc.
//
// The host connects to the FC vsock UDS, sends "CONNECT <port>\n", reads
// back "OK <host-port>\n", and from that point on the UDS is a transparent
// pipe to the guest's vsock port.
type VsockConnection struct {
	udsPath string // path to the firecracker vsock UDS
	port    uint32 // guest port the sysbox-init agent listens on
}

func (c *VsockConnection) OpenConsole(ctx context.Context, req ConsoleRequest) (*RawConsoleSession, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	cmd := req.Cmd
	if len(cmd) == 0 {
		shell := req.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = []string{shell}
	}
	if err := json.NewEncoder(conn).Encode(vsockrpc.Request{
		Op:   vsockrpc.OpConsole,
		Cmd:  cmd,
		Env:  req.Env,
		TTY:  true,
		Cols: req.Cols,
		Rows: req.Rows,
	}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send console request: %w", err)
	}
	return NewRawConsoleSession(conn), nil
}

// NewVsockConnection builds a vsock-based Connection for the given VM.
func NewVsockConnection(udsPath string, guestPort uint32) *VsockConnection {
	if guestPort == 0 {
		guestPort = vsockrpc.DefaultPort
	}
	return &VsockConnection{udsPath: udsPath, port: guestPort}
}

// dial opens a fresh UDS connection and performs the CONNECT handshake.
func (c *VsockConnection) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.udsPath)
	if err != nil {
		return nil, fmt.Errorf("dial vsock uds %s: %w", c.udsPath, err)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", c.port); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	// Read the OK <host-port>\n line.
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read CONNECT reply: %w", err)
	}
	if !strings.HasPrefix(line, "OK") {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock connect rejected: %q", strings.TrimSpace(line))
	}
	// br may have buffered bytes from the agent (it shouldn't until we send
	// a request, but be safe by wrapping the conn with the same reader).
	return &bufferedConn{Conn: conn, br: br}, nil
}

// WaitReady polls the agent with OpPing until it answers or the deadline elapses.
func (c *VsockConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := c.ping(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return fmt.Errorf("vsock agent not ready after %v: %w", timeout, lastErr)
}

func (c *VsockConnection) ping(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(vsockrpc.Request{Op: vsockrpc.OpPing}); err != nil {
		return err
	}
	dec := json.NewDecoder(conn)
	var f vsockrpc.Frame
	if err := dec.Decode(&f); err != nil {
		return err
	}
	if f.Error != "" {
		return fmt.Errorf("%s", f.Error)
	}
	if !f.Pong {
		return fmt.Errorf("unexpected ping reply: %+v", f)
	}
	return nil
}

// frameHandler is called for each frame received from an OpExec stream.
// Return io.EOF to stop reading (the connection is closed either way).
type frameHandler func(vsockrpc.Frame) error

// execFrameStream dials a fresh vsock connection, sends one OpExec request, and
// calls handler for each Frame the agent returns. Stdout/stderr payloads are
// delivered verbatim; the caller decides what to do with them. The final
// frame (Done=true) is also delivered. Returns nil when the command exits
// cleanly, or the first error from handler / transport.
func (c *VsockConnection) execFrameStream(ctx context.Context, cmd []string, env map[string]string, workDir string, handler frameHandler) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := vsockrpc.Request{Op: vsockrpc.OpExec, Cmd: cmd, Env: env, WorkDir: workDir}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	dec := json.NewDecoder(conn)
	for {
		var f vsockrpc.Frame
		if err := dec.Decode(&f); err != nil {
			if err == io.EOF {
				return fmt.Errorf("vsock connection closed before exit frame")
			}
			return fmt.Errorf("read frame: %w", err)
		}
		if err := handler(f); err != nil {
			if err == io.EOF {
				return nil // caller requested stop
			}
			return err
		}
		if f.Done {
			if f.Error != "" {
				return fmt.Errorf("%s", f.Error)
			}
			if f.ExitCode != 0 {
				return fmt.Errorf("exit code %d", f.ExitCode)
			}
			return nil
		}
	}
}

func (c *VsockConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (substrate.ExecResult, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	cmd, err := CommandArgv(req)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	result := substrate.ExecResult{}
	err = c.execFrameStream(ctx, cmd, req.Environment, req.WorkingDir, func(f vsockrpc.Frame) error {
		if len(f.Stdout) > 0 {
			result.Stdout += string(f.Stdout)
			if _, err := stdout.Write(f.Stdout); err != nil {
				return fmt.Errorf("write stdout: %w", err)
			}
		}
		if len(f.Stderr) > 0 {
			result.Stderr += string(f.Stderr)
			if _, err := stderr.Write(f.Stderr); err != nil {
				return fmt.Errorf("write stderr: %w", err)
			}
		}
		if f.Done {
			result.ExitCode = f.ExitCode
		}
		return nil
	})
	if err != nil && result.ExitCode == 0 {
		return result, err
	}
	return result, nil
}

func (c *VsockConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	// Wrap the command so it daemonises and prints its pid. nohup + & is the
	// classic POSIX recipe; we capture $! before exiting.
	command, err := RemoteCommand(req)
	if err != nil {
		return 0, err
	}
	shell := "nohup " + command + " >/dev/null 2>&1 & echo $!"
	conn, err := c.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(vsockrpc.Request{
		Op:  vsockrpc.OpExec,
		Cmd: []string{"sh", "-c", shell},
	}); err != nil {
		return 0, err
	}

	var stdoutBuf strings.Builder
	dec := json.NewDecoder(conn)
	for {
		var f vsockrpc.Frame
		if err := dec.Decode(&f); err != nil {
			return 0, err
		}
		if len(f.Stdout) > 0 {
			stdoutBuf.Write(f.Stdout)
		}
		if f.Done {
			if f.Error != "" {
				return 0, fmt.Errorf("%s", f.Error)
			}
			var pid int
			fmt.Sscanf(strings.TrimSpace(stdoutBuf.String()), "%d", &pid)
			return pid, nil
		}
	}
}

func (c *VsockConnection) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := vsockrpc.Request{
		Op:   vsockrpc.OpWriteFile,
		Path: dstPath,
		Mode: uint32(info.Mode().Perm()),
		Size: int64(len(data)),
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	dec := json.NewDecoder(conn)
	var f vsockrpc.Frame
	if err := dec.Decode(&f); err != nil {
		return err
	}
	if f.Error != "" {
		return fmt.Errorf("%s", f.Error)
	}
	return nil
}

// bufferedConn wraps a net.Conn with a pre-attached bufio.Reader so reads
// see any bytes already buffered during the CONNECT handshake.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.br.Read(p) }
