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
	"time"

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

func (c *vsockConn) Close() error                       { return unix.Close(c.fd) }
func (c *vsockConn) LocalAddr() net.Addr                { return nil }
func (c *vsockConn) RemoteAddr() net.Addr               { return nil }
func (c *vsockConn) SetDeadline(t time.Time) error      { return nil }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return nil }

func handleVsockConn(fd int) {
	conn := &vsockConn{fd: fd}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	headerLine, err := reader.ReadBytes('\n')
	if err != nil {
		logf("vsock read header: %v", err)
		return
	}

	var req vsockrpc.Request
	if err := json.Unmarshal(headerLine, &req); err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: fmt.Sprintf("bad request: %v", err)})
		return
	}

	switch req.Op {
	case vsockrpc.OpPing:
		sendFrame(conn, vsockrpc.Frame{Pong: true, Done: true})
	case vsockrpc.OpExec:
		handleExec(conn, req)
	case vsockrpc.OpWriteFile:
		handleWriteFile(conn, reader, req)
	case vsockrpc.OpReadFile:
		handleReadFile(conn, req)
	default:
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

func sendFrame(w io.Writer, f vsockrpc.Frame) {
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = w.Write(data)
}

func handleExec(conn net.Conn, req vsockrpc.Request) {
	if len(req.Cmd) == 0 {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: "empty cmd"})
		return
	}

	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}

	done := make(chan struct{}, 2)
	go pumpToFrames(conn, stdoutR, true, done)
	go pumpToFrames(conn, stderrR, false, done)

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
	sendFrame(conn, final)
}

func pumpToFrames(conn net.Conn, r io.Reader, isStdout bool, done chan<- struct{}) {
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
			sendFrame(conn, f)
		}
		if err != nil {
			return
		}
	}
}

func handleWriteFile(conn net.Conn, reader *bufio.Reader, req vsockrpc.Request) {
	if req.Path == "" {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: "missing path"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0644
	}
	f, err := os.OpenFile(req.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	defer f.Close()

	if req.Size > 0 {
		if _, err := io.CopyN(f, reader, req.Size); err != nil {
			sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
			return
		}
	}
	sendFrame(conn, vsockrpc.Frame{Done: true})
}

func handleReadFile(conn net.Conn, req vsockrpc.Request) {
	data, err := os.ReadFile(req.Path)
	if err != nil {
		sendFrame(conn, vsockrpc.Frame{Done: true, Error: err.Error()})
		return
	}
	sendFrame(conn, vsockrpc.Frame{Size: int64(len(data))})
	conn.Write(data)
	sendFrame(conn, vsockrpc.Frame{Done: true})
}
