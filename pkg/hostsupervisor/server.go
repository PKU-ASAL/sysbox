package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
)

// Server is a host-local Unix socket boundary. The socket owner/group defines
// which worker may submit requests; issuer signatures and canonical resource
// identity still authorize every effect. Private keys never cross this socket.
type Server struct {
	Socket          string
	GroupID         int
	Dispatcher      *Dispatcher
	Power           *PowerDispatcher
	Files           *FileDispatcher
	Runs            *RunCoordinator
	RunEffect       func(context.Context, RunRequest) (json.RawMessage, error)
	SSH             *SSHAccessCoordinator
	SSHApply        func(context.Context, controlplane.SSHAccessRequest) (json.RawMessage, error)
	SSHDelete       func(context.Context, controlplane.SSHAccessRequest) (json.RawMessage, error)
	OnRecoveryError func(error)
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Dispatcher == nil && s.Power == nil && s.Files == nil && s.Runs == nil && s.SSH == nil || !filepath.IsAbs(s.Socket) {
		return fmt.Errorf("absolute supervisor socket and dispatcher required")
	}
	directory := filepath.Dir(s.Socket)
	if err := EnsureOwnedDirectory(directory, 0750); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("supervisor socket directory must not be writable by worker")
	}
	lock := flock.New(s.Socket + ".lock")
	defer lock.Close()
	held, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("host supervisor already running")
	}
	if s.GroupID >= 0 {
		if err := os.Chown(directory, -1, s.GroupID); err != nil {
			return err
		}
	}
	if st, err := os.Lstat(s.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("supervisor socket path is not a socket")
		}
		// Only recover stale socket while holding this service's stable file lock.
		c, e := net.DialTimeout("unix", s.Socket, 100*time.Millisecond)
		if e == nil {
			c.Close()
			return fmt.Errorf("supervisor socket is active")
		}
		if err := os.Remove(s.Socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.Socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if s.GroupID >= 0 {
		if err := os.Chown(s.Socket, -1, s.GroupID); err != nil {
			return err
		}
	}
	if err := os.Chmod(s.Socket, 0660); err != nil {
		return err
	}
	return s.serveListener(ctx, listener)
}

func (s *Server) serveListener(parent context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer func() { cancel(); _ = listener.Close(); workers.Wait() }()
	if s.Dispatcher != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.recoveryLoop(ctx) }()
	}
	if s.Power != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, s.Power.Recover) }()
	}
	if s.Files != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, s.Files.Recover) }()
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		workers.Add(1)
		go func() { defer workers.Done(); defer conn.Close(); s.serveConnection(ctx, conn) }()
	}
}
func (s *Server) recoveryLoop(ctx context.Context) {
	s.runRecovery(ctx, s.Dispatcher.Recover)
}
func (s *Server) runRecovery(ctx context.Context, recover func(context.Context) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// Recover bounds each record independently so a slow first record
		// cannot repeatedly consume the deadline of all later records.
		err := recover(ctx)
		if err != nil && ctx.Err() == nil && s.OnRecoveryError != nil {
			s.OnRecoveryError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) serveConnection(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	request := Request{}
	decoder := json.NewDecoder(io.LimitReader(conn, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		json.NewEncoder(conn).Encode(Response{Version: 1, Error: "invalid host supervisor request"})
		return
	}
	response := s.Handle(ctx, request)
	_ = json.NewEncoder(conn).Encode(response)
}
func (s *Server) Handle(ctx context.Context, request Request) Response {
	response := Response{Version: 1}
	if request.Version != 1 || request.Target.Topology == "" || request.Target.Node == "" || strings.ContainsAny(request.Target.Topology, "/\\\x00") || request.Target.Scope.TopologyUID == "" || request.Target.Scope.ResourceUID == "" || request.Target.Scope.Generation < 1 {
		response.Error = "invalid supervisor target or protocol"
		return response
	}
	var err error
	var record TaskRecord
	switch request.Operation {
	case "info", "start", "observe", "cancel", "renew", "output":
		if s.Dispatcher == nil {
			response.Error = "host task supervisor unavailable"
			return response
		}
	case "run-start", "run-observe":
		r := request.RunRequest
		if r == nil || r.Topology != request.Target.Topology || r.TopologyUID != request.Target.Scope.TopologyUID || r.ResourceUID != request.Target.Scope.ResourceUID || r.Generation != request.Target.Scope.Generation {
			response.Error = "host run target mismatch"
			return response
		}
	}
	switch request.Operation {
	case "file-info":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.BootID, err = s.Files.BootID(ctx, request.Target)
	case "file-install":
		if s.Files == nil || request.FileRequest == nil || request.Grant == nil {
			err = fmt.Errorf("signed file request required")
			break
		}
		_, response.FileResult, err = s.Files.Install(ctx, request.Target, *request.FileRequest, *request.Grant)
	case "file-observe":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.FileResult, err = s.Files.Observe(ctx, request.Target, request.FileHandle)
	case "file-cancel":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.FileResult, err = s.Files.Cancel(ctx, request.Target, request.FileHandle)
	case "run-start":
		if s.Runs == nil || s.RunEffect == nil || request.RunRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host run coordinator unavailable")
			break
		}
		response.RunEvidence, err = s.Runs.Start(ctx, *request.RunRequest, *request.Grant, func(effectCtx context.Context) (json.RawMessage, error) {
			return s.RunEffect(effectCtx, *request.RunRequest)
		})
	case "run-observe":
		if s.Runs == nil || request.RunRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host run coordinator unavailable")
			break
		}
		response.RunEvidence, err = s.Runs.Observe(ctx, *request.RunRequest, *request.Grant)
	case "ssh-apply":
		if s.SSH == nil || s.SSHApply == nil || request.SSHRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host ssh access coordinator unavailable")
			break
		}
		response.SSHEvidence, err = s.SSH.Apply(ctx, *request.SSHRequest, *request.Grant, func(effectCtx context.Context) (json.RawMessage, error) {
			return s.SSHApply(effectCtx, *request.SSHRequest)
		})
	case "ssh-delete":
		if s.SSH == nil || s.SSHDelete == nil || request.SSHRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host ssh access coordinator unavailable")
			break
		}
		response.SSHEvidence, err = s.SSH.Delete(ctx, *request.SSHRequest, *request.Grant, func(effectCtx context.Context) (json.RawMessage, error) {
			return s.SSHDelete(effectCtx, *request.SSHRequest)
		})
	case "ssh-observe":
		if s.SSH == nil || request.SSHRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host ssh access coordinator unavailable")
			break
		}
		response.SSHEvidence, err = s.SSH.Observe(ctx, *request.SSHRequest, *request.Grant)
	case "host-info":
		if s.Power == nil {
			err = fmt.Errorf("host power supervisor unavailable")
			break
		}
		response.BootID, err = s.Power.BootID(ctx, request.Target)
	case "power-apply":
		if s.Power == nil || request.Power == nil || request.Grant == nil {
			err = fmt.Errorf("signed power request required")
			break
		}
		err = s.Power.Execute(ctx, request.Target, *request.Power, *request.Grant)
	case "power-observe":
		if s.Power == nil || request.Grant == nil {
			err = fmt.Errorf("signed power proof required")
			break
		}
		err = s.Power.Observe(ctx, request.Target, *request.Grant)
	case "info":
		response.BootID, err = s.Dispatcher.BootID(ctx, request.Target)
	case "start":
		if request.Task == nil {
			err = guesttask.ErrUnauthorized
			break
		}
		record, err = s.Dispatcher.Start(ctx, request.Target, *request.Task)
		response.Handle = record.Handle
		response.Result = record.Result
	case "observe":
		record, err = s.Dispatcher.Observe(ctx, request.Target, request.Handle)
		response.Handle = record.Handle
		response.Result = record.Result
	case "cancel":
		record, err = s.Dispatcher.Cancel(ctx, request.Target, request.Handle)
		response.Result = record.Result
	case "renew":
		if request.Grant == nil {
			err = guesttask.ErrUnauthorized
			break
		}
		record, err = s.Dispatcher.Renew(ctx, request.Target, request.Handle, *request.Grant)
		response.Result = record.Result
	case "output":
		response.Output, err = s.Dispatcher.ReadOutput(ctx, request.Target, request.Handle, request.Stream, request.Cursor, request.Limit)
	default:
		err = fmt.Errorf("unknown supervisor operation")
	}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}
