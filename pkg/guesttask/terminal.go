package guesttask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

const maxTerminalControl = 16 << 10
const terminalControlTimeout = time.Second

type terminalRuntime struct {
	path       string
	listener   *net.UnixListener
	stdin      *os.File
	master     *os.File
	childInput *os.File
	outputDone chan error
	wg         sync.WaitGroup
	closeOnce  sync.Once
}

func prepareTerminal(root string, cmd *exec.Cmd, options substrate.TerminalOptions, stdout, stderr io.Writer) (*terminalRuntime, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "terminal.sock")
	if len(path) >= 104 {
		return nil, fmt.Errorf("terminal control socket path is too long")
	}
	r := &terminalRuntime{path: path}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	if options.TTY {
		master, slave, err := pty.Open()
		if err != nil {
			return nil, err
		}
		fd, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			master.Close()
			slave.Close()
			return nil, err
		}
		if err = unix.SetNonblock(fd, true); err != nil {
			unix.Close(fd)
			master.Close()
			slave.Close()
			return nil, err
		}
		master.Close()
		master = os.NewFile(uintptr(fd), "terminal-master")
		r.master = master
		r.childInput = slave
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		cmd.SysProcAttr.Setctty, cmd.SysProcAttr.Ctty = true, 0
		if err = pty.Setsize(master, &pty.Winsize{Cols: uint16(options.Cols), Rows: uint16(options.Rows)}); err != nil {
			slave.Close()
			master.Close()
			return nil, err
		}
		r.outputDone = make(chan error, 1)
		go func() {
			_, err := io.Copy(stdout, master)
			if errors.Is(err, unix.EIO) {
				err = nil
			}
			r.outputDone <- err
		}()
		_ = stderr
	} else {
		reader, writer, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		r.stdin = writer
		r.childInput = reader
		cmd.Stdin = reader
		cmd.Stdout, cmd.Stderr = stdout, stderr
	}
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		r.Close()
		return nil, err
	}
	r.listener = listener
	if err = os.Chmod(path, 0600); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *terminalRuntime) Start() {
	_ = r.childInput.Close()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			conn, err := r.listener.AcceptUnix()
			if err != nil {
				return
			}
			r.handle(conn)
		}
	}()
}

func (r *terminalRuntime) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(terminalControlTimeout))
	var control substrate.TerminalControl
	err := json.NewDecoder(io.LimitReader(conn, maxTerminalControl)).Decode(&control)
	if err == nil {
		err = control.Validate()
	}
	if err == nil {
		switch control.Type {
		case "stdin":
			input := r.stdin
			if r.master != nil {
				input = r.master
			}
			if input == nil {
				err = fmt.Errorf("terminal input is closed")
			} else {
				if err = input.SetWriteDeadline(time.Now().Add(terminalControlTimeout)); err == nil {
					_, err = input.Write(control.Data)
				}
			}
		case "eof":
			if r.stdin != nil {
				err = r.stdin.Close()
			} else {
				err = fmt.Errorf("PTY EOF must be sent as terminal input")
			}
		case "resize":
			if r.master == nil {
				err = fmt.Errorf("resize requires a TTY")
			} else {
				err = pty.Setsize(r.master, &pty.Winsize{Cols: uint16(control.Cols), Rows: uint16(control.Rows)})
			}
		}
	}
	response := struct {
		Error string `json:"error,omitempty"`
	}{}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (r *terminalRuntime) Drain() error {
	if r.outputDone == nil {
		return nil
	}
	select {
	case err := <-r.outputDone:
		r.outputDone = nil
		return err
	case <-time.After(2 * time.Second):
		_ = r.master.Close()
		<-r.outputDone
		r.outputDone = nil
		return fmt.Errorf("terminal output did not finish after process termination")
	}
}

func (r *terminalRuntime) Close() error {
	r.closeOnce.Do(func() {
		if r.listener != nil {
			_ = r.listener.Close()
		}
		if r.childInput != nil {
			_ = r.childInput.Close()
		}
		if r.stdin != nil {
			_ = r.stdin.Close()
		}
		if r.master != nil {
			_ = r.master.Close()
		}
		r.wg.Wait()
		if r.outputDone != nil {
			<-r.outputDone
			r.outputDone = nil
		}
		_ = os.Remove(r.path)
	})
	return nil
}

func sendTerminalControl(ctx context.Context, path string, control substrate.TerminalControl) error {
	if err := control.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, terminalControlTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err = json.NewEncoder(conn).Encode(control); err != nil {
		return err
	}
	var response struct {
		Error string `json:"error,omitempty"`
	}
	if err = json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (e *Engine) Control(ctx context.Context, call Call) error {
	if call.Control == nil || call.Grant == nil {
		return ErrUnauthorized
	}
	if err := call.Control.Validate(); err != nil {
		return err
	}
	boot, err := BootID()
	if err != nil {
		return err
	}
	if err = verifyGrant(*call.Grant, e.PublicKey, boot, time.Now()); err != nil {
		return err
	}
	unlock, err := e.lock(ctx, call.ID)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := e.load(call.ID)
	if err != nil {
		return err
	}
	if r.Digest != call.Digest || r.Terminal == nil || r.TerminationConfirmed || r.CancelRequested || r.Status != "running" || remainingRecord(r) <= 0 || !reflect.DeepEqual(r.Grant, *call.Grant) {
		return ErrConflict
	}
	return sendTerminalControl(ctx, filepath.Join(e.taskPath(call.ID), "terminal.sock"), *call.Control)
}
