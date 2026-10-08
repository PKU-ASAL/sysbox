package guesttask

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func remainingGrant(g SignedGrant) time.Duration {
	budget, err := controlplane.ExecutionGrantBudget(g, time.Now())
	if err != nil {
		return 0
	}
	return budget
}

func bootNanos() (int64, error) {
	var now unix.Timespec
	err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &now)
	return now.Nano(), err
}
func remainingRecord(r Record) time.Duration {
	now, err := bootNanos()
	if err != nil {
		return 0
	}
	return time.Duration(r.DeadlineBootNS - now)
}

func launchGuard(e *Engine, r Record) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	args := []string{"--guest-task-guard"}
	if os.Getenv("SYSBOX_GUESTTASK_TEST_HELPER") == "1" {
		args = []string{"-test.run=^TestTaskWorkerProcess$"}
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "SYSBOX_GUESTTASK_GUARD=1")
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		write.Close()
		return err
	}
	write.Close()
	go cmd.Wait()
	_ = read.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready [1]byte
	if _, err = read.Read(ready[:]); err != nil {
		return err
	}
	if ready[0] != 1 {
		return ErrUnknown
	}
	return nil
}

// RunGuard is independent of both the RPC helper and task supervisor. It
// remains outside the task cgroup and stops the entire subtree if supervision
// disappears or the monotonic grant budget expires.
func RunGuard(root, cgroupRoot, id string) error {
	e := Engine{Root: root, CgroupRoot: cgroupRoot}
	r, err := e.load(id)
	if err != nil {
		return err
	}
	boot, err := BootID()
	if err != nil || boot != r.BootID {
		return ErrUnknown
	}
	pidfd, err := unix.PidfdOpen(r.SupervisorPID, 0)
	if err != nil {
		return err
	}
	defer unix.Close(pidfd)
	ticks, err := processStart(r.SupervisorPID)
	if err != nil || ticks != r.SupervisorStartTicks {
		return ErrConflict
	}
	group, err := e.openGroup(r)
	if err != nil {
		return err
	}
	defer group.Close()
	ready := os.NewFile(3, "guard-ready")
	if ready == nil {
		return fmt.Errorf("guard acknowledgement pipe unavailable")
	}
	if _, err = ready.Write([]byte{1}); err != nil {
		ready.Close()
		return err
	}
	ready.Close()
	deadline := time.Now().Add(remainingRecord(r))
	latestDeadline := r.DeadlineBootNS
	for {
		current, err := e.load(id)
		if err != nil {
			_ = killGroup(group)
			return err
		}
		if current.TerminationConfirmed {
			return nil
		}
		if current.DeadlineBootNS > latestDeadline {
			deadline = time.Now().Add(remainingRecord(current))
			latestDeadline = current.DeadlineBootNS
		}
		polls := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		_, err = unix.Poll(polls, 25)
		if err != nil && err != unix.EINTR {
			return err
		}
		if polls[0].Revents != 0 || current.CancelRequested || remainingRecord(current) <= 0 || !time.Now().Before(deadline) {
			if err = killGroup(group); err != nil {
				return err
			}
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				empty, err := groupEmpty(group)
				if err != nil {
					return err
				}
				if empty {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Give a living supervisor a chance to publish its wait result. If it is
			// gone, only absence is known; never invent a successful exit code.
			if polls[0].Revents == 0 {
				continue
			}
			unlock, err := e.lock(context.Background(), id)
			if err != nil {
				return err
			}
			defer unlock()
			final, err := e.load(id)
			if err != nil {
				return err
			}
			if final.TerminationConfirmed {
				return nil
			}
			final.TerminationConfirmed, err = groupEmpty(group)
			if err != nil {
				return err
			}
			final.Status = "unknown"
			final.ExitCodeKnown = false
			final.Error = "task supervisor exited before durable result"
			return e.save(final)
		}
	}
}

func (e *Engine) Renew(ctx context.Context, id, digest string, grant SignedGrant) (Record, error) {
	boot, err := BootID()
	if err != nil {
		return Record{}, err
	}
	if err = verifyGrant(grant, e.PublicKey, boot, time.Now()); err != nil {
		return Record{}, err
	}
	unlock, err := e.lock(ctx, id)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	r, err := e.load(id)
	if err != nil {
		return r, err
	}
	old := r.Grant.Grant
	next := grant.Grant
	if r.Digest != digest || next.Digest != digest || r.BootID != boot || r.TerminationConfirmed || r.CancelRequested || old.Scope != next.Scope || old.Epoch != next.Epoch || old.ParentEpoch != next.ParentEpoch || old.AgentID != next.AgentID || old.SessionID != next.SessionID || old.ActionID != next.ActionID || old.CommandID != next.CommandID || old.Attempt != next.Attempt || remainingRecord(r) <= 0 || next.ExpiresAt.Before(old.ExpiresAt) {
		return r, ErrUnauthorized
	}
	r.Grant = grant
	if next.ExpiresAt.After(old.ExpiresAt) {
		now, err := bootNanos()
		if err != nil {
			return r, err
		}
		budget := remainingGrant(grant)
		if budget <= 0 {
			return r, ErrUnauthorized
		}
		r.DeadlineBootNS = now + int64(budget)
	}
	return r, e.save(r)
}
