package guesttask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

type boundedFile struct {
	f         *os.File
	limit     int64
	size      int64
	truncated bool
	mu        sync.Mutex
}

func (w *boundedFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(p)
	remaining := w.limit - w.size
	if int64(len(p)) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	return total, nil
}
func (w *boundedFile) finish() (int64, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.f.Sync()
	closeErr := w.f.Close()
	if err == nil {
		err = closeErr
	}
	return w.size, w.truncated, err
}

// RunWorker is invoked only by the installed helper executable. The worker
// owns process waiting and the monotonic expiry timer independently of RPCs.
// It never relaunches a record past prepared; ambiguous startup is observed.
func RunWorker(root, cgroupRoot, id string) error {
	e := Engine{Root: root, CgroupRoot: cgroupRoot}
	ctx := context.Background()
	unlock, err := e.lock(ctx, id)
	if err != nil {
		return err
	}
	r, err := e.load(id)
	if err != nil {
		unlock()
		return err
	}
	if r.Status != "prepared" {
		unlock()
		return ErrConflict
	}
	boot, err := BootID()
	if err != nil || boot != r.BootID {
		unlock()
		return ErrUnknown
	}
	var req Request
	input := os.NewFile(3, "task-request")
	if input == nil {
		unlock()
		return ErrUnknown
	}
	raw, readErr := io.ReadAll(io.LimitReader(input, MaxInput*2+1))
	input.Close()
	if readErr != nil || len(raw) > MaxInput*2 {
		unlock()
		return fmt.Errorf("task request memory unavailable")
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		unlock()
		return err
	}
	digest, err := req.Digest()
	if err != nil || digest != r.Digest {
		unlock()
		return ErrConflict
	}
	if r.CancelRequested || remainingRecord(r) <= 0 {
		r.Status = "cancelled"
		r.TerminationConfirmed = true
		r.CancelRequested = true
		err = e.save(r)
		unlock()
		return err
	}
	if err = e.verifyCgroupRoot(); err != nil {
		unlock()
		return err
	}
	if err = os.Mkdir(e.group(id), 0700); err != nil {
		unlock()
		return err
	}
	r.CgroupInode, err = groupInode(e.group(id))
	if err != nil {
		unlock()
		return err
	}
	r.Status = "starting"
	r.SupervisorPID = os.Getpid()
	r.SupervisorStartTicks, err = processStart(r.SupervisorPID)
	if err != nil {
		unlock()
		return err
	}
	if err = e.save(r); err != nil {
		unlock()
		return err
	}
	group, err := e.openGroup(r)
	if err != nil {
		unlock()
		return err
	}
	defer group.Close()
	// Kernel 5.10 uses the explicit freeze+pidfd subtree retirement path.
	kill, err := groupFile(group, "cgroup.kill", syscall.O_WRONLY)
	if err != nil {
		if err != syscall.ENOENT {
			unlock()
			return err
		}
		kill, err = groupFile(group, "cgroup.freeze", syscall.O_WRONLY)
		if err != nil {
			unlock()
			return err
		}
	}
	kill.Close()
	if err = launchGuard(&e, r); err != nil {
		unlock()
		return err
	}
	stdout, err := os.OpenFile(filepath.Join(e.taskPath(id), "stdout"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		unlock()
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(e.taskPath(id), "stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		unlock()
		return err
	}
	defer stderr.Close()
	out := &boundedFile{f: stdout, limit: int64(req.OutputLimit)}
	errOut := &boundedFile{f: stderr, limit: int64(req.OutputLimit)}
	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Dir = req.WorkingDir
	cmd.Env = os.Environ()
	for key, value := range req.Environment {
		if strings.ContainsAny(key, "=\x00") || key == "" {
			unlock()
			return fmt.Errorf("invalid task environment")
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin = bytes.NewReader(req.Input)
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, UseCgroupFD: true, CgroupFD: int(group.Fd()), Credential: &syscall.Credential{Uid: req.UserID, Gid: req.UserID, NoSetGroups: false}}
	if remainingRecord(r) <= 0 {
		r.Status = "cancelled"
		r.CancelRequested = true
		r.TerminationConfirmed = true
		err = e.save(r)
		unlock()
		return err
	}
	runtime.LockOSThread()
	err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	if err == nil {
		err = cmd.Start()
	}
	runtime.UnlockOSThread()
	if err != nil {
		r.Status = "failed"
		r.Error = err.Error()
		r.TerminationConfirmed = true
		_ = e.save(r)
		unlock()
		return err
	}
	r.PID = cmd.Process.Pid
	r.StartTicks, err = processStart(r.PID)
	if err != nil {
		_ = killGroup(group)
		_ = cmd.Wait()
		r.Status = "unknown"
		r.Error = "process anchor unavailable"
		_ = e.save(r)
		unlock()
		return err
	}
	r.Status = "running"
	if err = e.save(r); err != nil {
		_ = killGroup(group)
		_ = cmd.Wait()
		unlock()
		return err
	}
	remaining := remainingRecord(r)
	latestDeadline := r.DeadlineBootNS
	deadline := time.NewTimer(remaining)
	defer deadline.Stop()
	unlock()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var waitErr error
	cancelled := false
running:
	for {
		select {
		case waitErr = <-waited:
			break running
		case <-deadline.C:
			current, loadErr := e.load(id)
			if loadErr == nil && !current.CancelRequested && remainingRecord(current) > 0 {
				deadline.Reset(remainingRecord(current))
				latestDeadline = current.DeadlineBootNS
				continue
			}
			cancelled = true
			_ = killGroup(group)
			waitErr = <-waited
			break running
		case <-ticker.C:
			current, loadErr := e.load(id)
			if loadErr != nil || current.CancelRequested || remainingRecord(current) <= 0 {
				cancelled = true
				_ = killGroup(group)
				waitErr = <-waited
				break running
			}
			if current.DeadlineBootNS > latestDeadline {
				deadline.Reset(remainingRecord(current))
				latestDeadline = current.DeadlineBootNS
			}
		}
	}
	// The entry process exiting does not imply its detached descendants exited.
	// Always retire the owned cgroup before publishing a terminal result.
	if err = killGroup(group); err != nil {
		return err
	}
	until := time.Now().Add(5 * time.Second)
	empty := false
	for time.Now().Before(until) {
		empty, err = groupEmpty(group)
		if err != nil {
			return err
		}
		if empty {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	outSize, outTruncated, outErr := out.finish()
	errSize, errTruncated, stderrErr := errOut.finish()
	unlock, err = e.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := e.load(id)
	if err != nil {
		return err
	}
	current.StdoutBytes, current.StderrBytes = outSize, errSize
	current.StdoutTruncated, current.StderrTruncated = outTruncated, errTruncated
	current.TerminationConfirmed = empty
	current.CancelRequested = current.CancelRequested || cancelled
	if !empty {
		current.Status = "unknown"
		current.Error = "task cgroup did not become empty"
	} else if cancelled || current.CancelRequested {
		current.Status = "cancelled"
	} else {
		current.Status = "exited"
		current.ExitCode = cmd.ProcessState.ExitCode()
		current.ExitCodeKnown = current.ExitCode >= 0
		if waitErr != nil && current.ExitCode < 0 {
			current.Error = waitErr.Error()
		}
	}
	if outErr != nil || stderrErr != nil {
		current.Status = "unknown"
		current.Error = "output durability failed"
	}
	return e.save(current)
}

func (e *Engine) Handle(ctx context.Context, call Call) Response {
	response := Response{Version: Version}
	var err error
	if call.Version != Version {
		response.Error = "guest task protocol requires rebuild"
		return response
	}
	switch call.Operation {
	case "file-info":
		response.BootID, err = BootID()
	case "file-install":
		if call.FileRequest == nil || call.Grant == nil || int64(len(call.FileData)) > substrate.MaxFileInstallSize {
			err = ErrConflict
		} else {
			var record FileInstallRecord
			record, err = e.InstallFile(ctx, *call.FileRequest, *call.Grant, bytes.NewReader(call.FileData))
			response.FileRecord = &record
		}
	case "file-observe":
		if call.FileHandle == nil {
			err = ErrConflict
		} else {
			var record FileInstallRecord
			record, err = e.ObserveFileInstall(ctx, *call.FileHandle)
			response.FileRecord = &record
		}
	case "file-inspect":
		if call.FileHandle == nil {
			err = ErrConflict
		} else {
			var inspection substrate.InstalledFileInspection
			inspection, err = e.InspectInstalledFile(ctx, *call.FileHandle)
			if err == nil {
				response.FileInspection = &inspection
				copy := *call.FileHandle
				response.FileHandle = &copy
			}
		}
	case "file-cancel":
		if call.FileHandle == nil {
			err = ErrConflict
		} else {
			var record FileInstallRecord
			if call.Grant != nil {
				record, err = e.CancelFileInstall(ctx, *call.FileHandle, *call.Grant)
			} else {
				record, err = e.CancelFileInstall(ctx, *call.FileHandle)
			}
			response.FileRecord = &record
		}
	case "ssh-access":
		if call.SSHMutation == nil || call.Grant == nil {
			err = ErrConflict
		} else {
			var result substrate.SSHAccessResult
			result, err = e.MutateSSHAccess(ctx, *call.SSHMutation, *call.Grant)
			response.SSHResult = &result
		}
	case "ssh-discover":
		if call.SSHQuery == nil {
			err = ErrConflict
		} else {
			var discovered substrate.SSHAccessDiscovery
			discovered, err = e.DiscoverSSHAccess(ctx, *call.SSHQuery)
			response.SSHDiscovery = &discovered
		}
	case "ssh-observe":
		if call.SSHHandle == nil {
			err = ErrConflict
		} else {
			var result substrate.SSHAccessResult
			result, err = e.ObserveSSHAccess(ctx, *call.SSHHandle)
			response.SSHResult = &result
		}
	case "ssh-inspect":
		if call.SSHHandle == nil {
			err = ErrConflict
		} else {
			var inspection substrate.SSHAccessInspection
			inspection, err = e.InspectSSHAccess(ctx, *call.SSHHandle)
			response.SSHInspection = &inspection
			if err == nil {
				copy := *call.SSHHandle
				response.SSHHandle = &copy
			}
		}
	case "ssh-authorized-keys":
		response.SSHKeys, err = e.SSHAuthorizedKeys(ctx, call.SSHUser, call.SSHAddress, call.SSHPort)
	case "info":
		response.BootID, err = BootID()
		if err == nil {
			err = e.verifyCgroupRoot()
		}
	case "start":
		if call.Request == nil {
			err = ErrConflict
		} else {
			var r Record
			r, err = e.Start(ctx, *call.Request)
			response.Record = &r
		}
	case "observe":
		var r Record
		r, err = e.Observe(ctx, call.ID, call.Digest)
		response.Record = &r
	case "cancel":
		err = e.CancelWithProof(ctx, call)
		if err == nil {
			var r Record
			r, err = e.Observe(ctx, call.ID, call.Digest)
			response.Record = &r
		}
	case "renew":
		if call.Grant == nil {
			err = ErrUnauthorized
		} else {
			var r Record
			r, err = e.Renew(ctx, call.ID, call.Digest, *call.Grant)
			response.Record = &r
		}
	case "list":
		response.Records, err = e.List(ctx)
	case "output":
		response.Output, response.NextCursor, err = e.ReadOutput(ctx, call.ID, call.Digest, call.Stream, call.Cursor, call.Limit)
	default:
		err = fmt.Errorf("unknown guest task operation")
	}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

// ReadCall imposes a hard framing limit before JSON decoding.
func ReadCall(reader io.Reader) (Call, error) {
	var call Call
	raw, err := io.ReadAll(io.LimitReader(reader, MaxCallSize+1))
	if err != nil {
		return call, err
	}
	if len(raw) > MaxCallSize {
		return call, fmt.Errorf("oversized task request")
	}
	err = json.Unmarshal(raw, &call)
	return call, err
}
