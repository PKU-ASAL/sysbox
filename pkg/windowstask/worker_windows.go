package windowstask

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/oslab/sysbox/pkg/taskprotocol"
	"golang.org/x/sys/windows"
)

const jobListAttribute = 0x0002000d

type outputCapture struct {
	file      *os.File
	limit     int64
	bytes     int64
	truncated bool
}

func (c *outputCapture) Write(data []byte) (int, error) {
	total := len(data)
	room := c.limit - c.bytes
	if int64(len(data)) > room {
		data = data[:room]
		c.truncated = true
	}
	n, err := c.file.Write(data)
	c.bytes += int64(n)
	return total, err
}
func createPrivateJob(name string) (windows.Handle, error) {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)")
	if err != nil {
		return 0, err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateJobObjectW").Call(uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(wide)))
	if h == 0 {
		return 0, callErr
	}
	if callErr == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(windows.Handle(h))
		return 0, ErrConflict
	}
	job := windows.Handle(h)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// RunWorker lives independently of the QGA RPC process. Job-list creation
// joins the process to its Job Object atomically; no suspended orphan window.
func (e *Engine) RunWorker(ctx context.Context, request taskprotocol.Request, ready io.Writer) (resultErr error) {
	p, ok := e.Platform.(*WindowsPlatform)
	if !ok {
		return ErrUnauthorized
	}
	unlock, err := e.lock(ctx)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	digest, _, err := e.verify(request)
	if err != nil {
		return err
	}
	r, err := e.load(request.ID)
	if err != nil {
		return err
	}
	if r.Digest != digest || !sameGrant(r.Grant.Grant, request.Grant.Grant) || r.Status != "prepared" || r.CancelRequested || int64(p.Uptime()) >= r.DeadlineBootNS {
		return ErrConflict
	}
	r.SupervisorPID = os.Getpid()
	r.SupervisorStartTicks, _, err = processCreation(r.SupervisorPID)
	if err != nil {
		return err
	}
	r.Status = "starting"
	if err = e.save(r); err != nil {
		return err
	}
	processCreated := false
	defer func() {
		if resultErr != nil && !processCreated {
			r.Status = "failed"
			r.TerminationConfirmed = true
			r.ExitCodeKnown = false
			r.Error = "worker rejected task before process creation"
			if saveErr := e.save(r); saveErr != nil {
				resultErr = fmt.Errorf("%v; persist pre-launch failure: %w", resultErr, saveErr)
			}
		}
	}()
	job, err := createPrivateJob(taskJobName(r))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	token, err := p.workerToken()
	if err != nil {
		return err
	}
	defer token.Close()
	if err = p.launchGuard(request.ID); err != nil {
		return err
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdinR.Close()
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stderrR.Close()
	defer stderrW.Close()
	inherited := []windows.Handle{windows.Handle(stdinR.Fd()), windows.Handle(stdoutW.Fd()), windows.Handle(stderrW.Fd())}
	for _, h := range inherited {
		if err = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return err
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherited[0]), uintptr(len(inherited))*unsafe.Sizeof(inherited[0])); err != nil {
		return err
	}
	if err = attrs.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return fmt.Errorf("atomic Job Object assignment requires Windows 10/Server 2016 or newer: %w", err)
	}
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = inherited[0], inherited[1], inherited[2]
	si.ProcThreadAttributeList = attrs.List()
	program, err := exec.LookPath(request.Args[0])
	if err != nil {
		return err
	}
	app, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(request.Args))
	if err != nil {
		return err
	}
	env, err := windowsEnvironment(request.Environment)
	if err != nil {
		return err
	}
	directory := request.WorkingDir
	if directory == "" {
		directory = os.Getenv("TEMP")
	}
	workdir, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return err
	}
	outFile, err := os.OpenFile(filepath.Join(e.Root, r.ID, "stdout"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer outFile.Close()
	errFile, err := os.OpenFile(filepath.Join(e.Root, r.ID, "stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer errFile.Close()
	out := &outputCapture{file: outFile, limit: int64(request.OutputLimit)}
	errOut := &outputCapture{file: errFile, limit: int64(request.OutputLimit)}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err = e.checkLaunchDeadline(ctx, r); err != nil {
		return err
	}
	if err = windows.CreateProcessAsUser(token, app, command, nil, nil, true, flags, &env[0], workdir, &si.StartupInfo, &process); err != nil {
		return err
	}
	processCreated = true
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	// Any later worker failure kills the exact job. Its independent guard also
	// holds a handle and verifies subtree emptiness if this supervisor dies.
	defer func() {
		if resultErr != nil {
			_ = windows.TerminateJobObject(job, 1)
		}
	}()
	r.PID = int(process.ProcessId)
	r.StartTicks, _, err = processCreation(r.PID)
	if err != nil {
		return err
	}
	r.Status = "running"
	if err = e.save(r); err != nil {
		return err
	}
	if err = e.checkLaunchDeadline(ctx, r); err != nil {
		return err
	}
	if _, err = windows.ResumeThread(process.Thread); err != nil {
		return err
	}
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	var copies sync.WaitGroup
	copies.Add(3)
	var copyErrors [3]error
	go func() {
		defer copies.Done()
		_, copyErrors[0] = io.Copy(stdinW, bytes.NewReader(request.Input))
		stdinW.Close()
	}()
	go func() { defer copies.Done(); _, copyErrors[1] = io.Copy(out, stdoutR) }()
	go func() { defer copies.Done(); _, copyErrors[2] = io.Copy(errOut, stderrR) }()
	locked = false
	unlock()
	// Lost acknowledgement is not a reason to stop supervising the workload.
	_, _ = io.WriteString(ready, "READY\n")
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	cancelled := false
	for {
		state, err := windows.WaitForSingleObject(process.Process, 0)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			break
		}
		select {
		case <-ctx.Done():
			cancelled = true
		case <-ticker.C:
		}
		current, err := e.load(r.ID)
		if err != nil {
			return err
		}
		if cancelled || current.CancelRequested || int64(p.Uptime()) >= current.DeadlineBootNS {
			cancelled = true
			if err = windows.TerminateJobObject(job, 1); err != nil {
				return err
			}
		}
	}
	// Root-process exit alone is insufficient: kill/reap any surviving child.
	if err = windows.TerminateJobObject(job, 1); err != nil {
		return err
	}
	until := time.Now().Add(5 * time.Second)
	for {
		empty, err := jobEmpty(job)
		if err != nil {
			return err
		}
		if empty {
			break
		}
		if time.Now().After(until) {
			return ErrUnknown
		}
		time.Sleep(10 * time.Millisecond)
	}
	stdinW.Close()
	copies.Wait()
	if copyErrors[1] != nil || copyErrors[2] != nil {
		return fmt.Errorf("task output could not be captured")
	}
	if err = outFile.Sync(); err != nil {
		return err
	}
	if err = errFile.Sync(); err != nil {
		return err
	}
	var exit uint32
	if err = windows.GetExitCodeProcess(process.Process, &exit); err != nil {
		return err
	}
	unlock, err = e.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	current, err := e.load(r.ID)
	if err != nil {
		return err
	}
	if current.Digest != r.Digest || current.SupervisorPID != r.SupervisorPID || current.SupervisorStartTicks != r.SupervisorStartTicks {
		return ErrConflict
	}
	before := current
	current.TerminationConfirmed = true
	current.StdoutBytes, current.StderrBytes = out.bytes, errOut.bytes
	current.StdoutTruncated, current.StderrTruncated = out.truncated, errOut.truncated
	current.CancelRequested = current.CancelRequested || cancelled
	if current.CancelRequested {
		current.Status = "cancelled"
		current.ExitCodeKnown = false
	} else {
		current.Status = "exited"
		current.ExitCodeKnown = true
		current.ExitCode = int(exit)
	}
	return e.save(mergeWorkerResult(before, current))
}
func (p *WindowsPlatform) launchGuard(id string) error {
	binary, err := os.Executable()
	if p.HelperBinary != "" {
		binary = p.HelperBinary
	}
	if err != nil {
		return err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(binary, "--guard", "--config", p.ConfigPath, "--id", id)
	cmd.Stdout = write
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	write.Close()
	go cmd.Wait()
	result := make(chan error, 1)
	go func() {
		s, err := bufio.NewReader(read).ReadString('\n')
		if err == nil && s != "READY\n" {
			err = ErrUnknown
		}
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("Windows job guard acknowledgement missing")
	}
}
func (e *Engine) RunGuard(ctx context.Context, id string, ready io.Writer) error {
	p, ok := e.Platform.(*WindowsPlatform)
	if !ok {
		return ErrUnauthorized
	}
	r, err := e.load(id)
	if err != nil {
		return err
	}
	boot, err := p.BootID()
	if err != nil || boot != r.BootID {
		return ErrUnknown
	}
	job, err := openTaskJob(r, 0x0004|0x0008)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	if _, err = io.WriteString(ready, "READY\n"); err != nil {
		return err
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		current, err := e.load(id)
		if err != nil {
			return err
		}
		if current.TerminationConfirmed {
			return nil
		}
		created, alive, err := processCreation(r.SupervisorPID)
		lost := err != nil || !alive || created != r.SupervisorStartTicks
		if lost || current.CancelRequested || int64(p.Uptime()) >= current.DeadlineBootNS {
			unlock, err := e.lock(ctx)
			if err != nil {
				return err
			}
			current, err = e.load(id)
			if err != nil {
				unlock()
				return err
			}
			if !current.TerminationConfirmed {
				current.CancelRequested = true
				if err = e.save(current); err != nil {
					unlock()
					return err
				}
				current, err = p.Stop(ctx, current)
				if err == nil {
					current.Error = "guard stopped task after supervisor loss, cancellation or expiry"
					err = e.save(current)
				}
			}
			unlock()
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
