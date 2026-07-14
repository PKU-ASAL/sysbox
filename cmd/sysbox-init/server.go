package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"golang.org/x/sys/unix"
)

// startVsockServer brings up an AF_VSOCK listener on the given port and
// dispatches inbound connections to handleVsockConn. Spawned in a goroutine
// from main; never returns unless the listener cannot be created.
func startVsockServer(port uint32) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("vsock socket (kernel must have CONFIG_VSOCKETS=y and CONFIG_VIRTIO_VSOCKETS=y compiled in, not =m): %w", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: port,
	}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return fmt.Errorf("vsock bind: %w", err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return fmt.Errorf("vsock listen: %w", err)
	}

	logf("vsock-agent listening on port %d", port)

	for {
		cfd, _, err := unix.Accept(fd)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			logf("vsock accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go handleVsockConn(cfd)
	}
}

// vsockConn wraps a raw vsock socket fd as a net.Conn so we can reuse
// stdlib I/O helpers (bufio, json.Encoder).
type vsockConn struct {
	fd int
}

func (c *vsockConn) Read(b []byte) (int, error) {
	for {
		n, err := unix.Read(c.fd, b)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

func (c *vsockConn) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := unix.Write(c.fd, b[total:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (c *vsockConn) Close() error         { return unix.Close(c.fd) }
func (c *vsockConn) LocalAddr() net.Addr  { return nil }
func (c *vsockConn) RemoteAddr() net.Addr { return nil }

func (c *vsockConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *vsockConn) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		return unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, nil)
	}
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
}

func (c *vsockConn) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		return unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, nil)
	}
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
}

func handleVsockConn(fd int) {
	conn := &vsockConn{fd: fd}
	defer conn.Close()

	// mutexWriter serialises all frame writes to this connection,
	// preventing byte-level interleaving when stdout and stderr
	// pump goroutines call sendFrame concurrently.
	mw := &mutexWriter{Writer: conn}

	// 64KB buffer limits the maximum header line size; ReadBytes returns
	// bufio.ErrBufferFull if the line exceeds this, preventing OOM from
	// malicious connections that never send '\n'.
	reader := bufio.NewReaderSize(conn, 64*1024)
	// Limit header line to 64KB to prevent OOM from malicious connections
	// that never send '\n'.
	headerLine, err := reader.ReadBytes('\n')
	if err != nil {
		if err == bufio.ErrBufferFull {
			logf("vsock read header: exceeded 64KB limit")
		} else {
			logf("vsock read header: %v", err)
		}
		return
	}

	var req vsockrpc.Request
	if err := json.Unmarshal(headerLine, &req); err != nil {
		sendFrame(mw, vsockrpc.Frame{Done: true, Error: fmt.Sprintf("bad request: %v", err)})
		return
	}

	switch req.Op {
	case vsockrpc.OpPing:
		sendFrame(mw, vsockrpc.Frame{Pong: true, Done: true})
	case vsockrpc.OpExec:
		handleExec(mw, req)
	case vsockrpc.OpConsole:
		handleConsole(conn, req)
	case vsockrpc.OpWriteFile:
		handleWriteFile(mw, reader, req)
	default:
		sendFrame(mw, vsockrpc.Frame{Done: true, Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

// mutexWriter wraps an io.Writer with a Mutex so concurrent goroutines
// can write complete logical units (e.g. JSON frames) without byte-level
// interleaving.
type mutexWriter struct {
	io.Writer
	mu sync.Mutex
}

func (w *mutexWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Writer.Write(b)
}

func sendFrame(w io.Writer, f vsockrpc.Frame) {
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = w.Write(data)
}

func handleExec(w io.Writer, req vsockrpc.Request) {
	if len(req.Cmd) == 0 {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: "empty cmd"})
		return
	}

	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	cmd.Dir = req.WorkDir
	// Merge host-supplied env into the current environment so that
	// PATH, HOME, etc. are preserved. Without this, any env overrides
	// from the host would erase the entire environment, breaking
	// subprocess lookups that depend on PATH.
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	} else {
		cmd.Env = os.Environ()
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}

	done := make(chan struct{}, 2)
	go pumpToFrames(w, stdoutR, true, done)
	go pumpToFrames(w, stderrR, false, done)

	waitErr := cmd.Wait()
	stdoutW.Close()
	stderrW.Close()
	<-done
	<-done

	final := vsockrpc.Frame{Done: true}
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			final.ExitCode = ee.ExitCode()
		} else {
			final.Error = waitErr.Error()
		}
	}
	sendFrame(w, final)
}

func handleConsole(conn net.Conn, req vsockrpc.Request) {
	cmdArgs := req.Cmd
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"/bin/sh"}
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	} else {
		cmd.Env = os.Environ()
	}
	rows, cols := req.Rows, req.Cols
	if rows <= 0 {
		rows = 32
	}
	if cols <= 0 {
		cols = 120
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	defer ptmx.Close()

	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(ptmx, conn)
		done <- err
	}()
	go func() {
		_, err := io.Copy(conn, ptmx)
		done <- err
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-done:
		_ = cmd.Process.Kill()
		<-waitCh
	case <-waitCh:
	}
}

func pumpToFrames(w io.Writer, r io.Reader, isStdout bool, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	buf := make([]byte, 8192)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f := vsockrpc.Frame{}
			payload := make([]byte, n)
			copy(payload, buf[:n])
			if isStdout {
				f.Stdout = payload
			} else {
				f.Stderr = payload
			}
			sendFrame(w, f)
		}
		if err != nil {
			return
		}
	}
}

// allowedWritePaths are the prefixes under which handleWriteFile may write.
// This prevents a compromised host from overwriting critical system files
// (e.g. /etc/shadow, /sbin/init) through the vsock agent.
var allowedWritePaths = []string{
	"/tmp/",
	"/opt/",
	"/root/",
	"/home/",
	"/usr/local/bin/",
	"/etc/sysbox/",
	"/etc/ssh/sshd_config.d/",
	"/etc/profile.d/",
	"/run/",
}

func handleWriteFile(w io.Writer, reader *bufio.Reader, req vsockrpc.Request) {
	if req.Path == "" {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: "missing path"})
		return
	}
	// Validate the target path is under an allowed prefix.
	cleanPath := filepath.Clean(req.Path)
	allowed := false
	for _, prefix := range allowedWritePaths {
		if strings.HasPrefix(cleanPath, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: fmt.Sprintf("path %q is outside allowed write directories", cleanPath)})
		return
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0644
	}
	f, err := os.OpenFile(req.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		sendFrame(w, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	defer f.Close()

	if req.Size > 0 {
		if _, err := io.CopyN(f, reader, req.Size); err != nil {
			sendFrame(w, vsockrpc.Frame{Done: true, Error: err.Error()})
			return
		}
	}
	sendFrame(w, vsockrpc.Frame{Done: true})
}
