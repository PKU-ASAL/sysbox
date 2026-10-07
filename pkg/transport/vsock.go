package transport

import (
	"bufio"
	"context"
	"crypto/sha256"
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

// abortConnOnContext closes conn as soon as ctx is done, and returns a stop
// function the caller must invoke once its reads are finished.
//
// Reads on a bare net.Conn consult neither a context nor a deadline, and
// json.Decoder / bufio.Reader take no context either. A peer that accepts the
// connection and then goes silent — a wedged vsock mux, a guest command that
// never writes and never exits — therefore blocks the read forever, which makes
// GuestExecutionRequest.TimeoutSeconds unenforceable on this path. Closing the
// connection is the only way to unblock such a read.
//
// This deliberately covers cancellation as well as deadlines;
// SetReadDeadline alone would leave a plain cancel unenforced.
func abortConnOnContext(ctx context.Context, conn io.Closer) (stop func()) {
	if ctx.Done() == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	return func() { close(stopped) }
}

// VsockConnection implements Connection by dialling a Firecracker-style
// vsock UDS socket and speaking the protocol defined in pkg/vsockrpc.
//
// The host connects to the FC vsock UDS, sends "CONNECT <port>\n", reads
// back "OK <host-port>\n", and from that point on the UDS is a transparent
// pipe to the guest's vsock port.
type VsockConnection struct {
	executionIdentity
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
	// DialContext bounds only the connect; the handshake below is a read on a
	// bare conn, so it needs its own bound or a silent peer hangs us forever.
	stopAbort := abortConnOnContext(ctx, conn)
	defer stopAbort()
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", c.port); err != nil {
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("send CONNECT to %s: %w", c.udsPath, ctxErr)
		}
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	// Read the OK <host-port>\n line.
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		// Attribute to the deadline rather than to the read it caused.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("read CONNECT reply from %s: %w", c.udsPath, ctxErr)
		}
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

// WaitReady requires initialization evidence; an RPC pong is only liveness.
func (c *VsockConnection) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for guest initialization (%v): %w", last, ctx.Err())
		default:
		}
		if err := c.guestReady(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for guest initialization (%v): %w", last, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *VsockConnection) guestReady(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer abortConnOnContext(ctx, conn)()

	if err := json.NewEncoder(conn).Encode(vsockrpc.Request{Op: vsockrpc.OpReady}); err != nil {
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
	if !f.Done || !f.Ready {
		return fmt.Errorf("guest initialization incomplete: %s", f.ReadinessReason)
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
func (c *VsockConnection) execFrameStream(ctx context.Context, cmd []string, env map[string]string, workDir string, stdin []byte, handler frameHandler) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The frame loop below reads a bare conn with no deadline: an agent that
	// accepts the work and never reports a frame would block it forever.
	defer abortConnOnContext(ctx, conn)()

	req := vsockrpc.Request{Op: vsockrpc.OpExec, Cmd: cmd, Env: env, WorkDir: workDir, StdinSize: int64(len(stdin))}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	if _, err := io.Copy(conn, strings.NewReader(string(stdin))); err != nil {
		return fmt.Errorf("send stdin: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for {
		var f vsockrpc.Frame
		if !scanner.Scan() {
			// Attribute to the context when it is what ended the read, so a
			// timeout is not reported as a peer protocol failure.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("read frame: %w", ctxErr)
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read frame: %w", err)
			}
			return fmt.Errorf("vsock connection closed before exit frame")
		}
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
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
			return nil
		}
	}
}

func (c *VsockConnection) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (result substrate.ExecResult, err error) {
	ctx, cancel := req.Context(ctx)
	defer cancel()
	defer func() { result, err = substrate.FinishExecution(ctx, result, err) }()
	cmd, err := CommandArgv(req)
	if err != nil {
		return substrate.ExecResult{ErrorClass: substrate.ExecErrorInvalid}, err
	}
	stdin, err := readVsockStdin(ctx, req.Stdin)
	if err != nil {
		return substrate.ExecResult{ErrorClass: substrate.ExecErrorInvalid}, err
	}
	if err := c.WaitReady(ctx, 30*time.Second); err != nil {
		return result, err
	}
	out := substrate.NewExecCapture(req.OutputLimitBytes, stdout)
	errOut := substrate.NewExecCapture(req.OutputLimitBytes, stderr)
	err = c.execFrameStream(ctx, cmd, req.Environment, req.WorkingDir, stdin, func(f vsockrpc.Frame) error {
		if len(f.Stdout) > 0 {
			if _, err := out.Write(f.Stdout); err != nil {
				return fmt.Errorf("write stdout: %w", err)
			}
		}
		if len(f.Stderr) > 0 {
			if _, err := errOut.Write(f.Stderr); err != nil {
				return fmt.Errorf("write stderr: %w", err)
			}
		}
		if f.Done {
			result.ExitCode = f.ExitCode
			result.ExitCodeKnown = f.Error == ""
		}
		return nil
	})
	result.Stdout, result.Stderr = out.String(), errOut.String()
	result.StdoutTruncated, result.StderrTruncated = out.Truncated(), errOut.Truncated()
	return result, err
}

func (c *VsockConnection) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	handle, err := c.Start(ctx, req)
	return handle.PID, err
}

func (c *VsockConnection) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	return startPOSIX(ctx, c, c.executionIdentity, c.udsPath, req)
}

func (c *VsockConnection) CopyFile(ctx context.Context, srcPath, dstPath string) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	file, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source must be a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := c.WaitReady(ctx, 30*time.Second); err != nil {
		return err
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer abortConnOnContext(ctx, conn)()

	req := vsockrpc.Request{
		SHA256: fmt.Sprintf("%x", digest.Sum(nil)),
		Op:     vsockrpc.OpWriteFile,
		Path:   dstPath,
		Mode:   uint32(info.Mode().Perm()),
		Size:   info.Size(),
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	if _, err := io.CopyN(conn, file, info.Size()); err != nil {
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
	if !f.Done || f.SHA256 != req.SHA256 {
		return fmt.Errorf("file transfer lacks matching terminal checksum")
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
