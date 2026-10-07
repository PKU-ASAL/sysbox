package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

// Recovery never redelivers an acknowledged non-idempotent command. The local
// inbox may prove acceptance but cannot prove whether an interrupted guest
// effect happened. Such work becomes terminal with an explicit unknown result.
func (s *AgentService) ReconcileLostCommands(ctx context.Context, now time.Time) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	commands, err := s.store.AgentCommandRecoveryPage(ctx, s.recoveryCursor)
	if err != nil {
		return
	}
	for _, cmd := range commands {
		s.recoveryCursor = cmd.ID
		if cmd.Type == "run_assigned" && cmd.Run != nil {
			run, err := s.store.GetRun(ctx, cmd.Run.ID)
			if err != nil {
				continue
			}
			if run.Status.IsTerminal() {
				s.ReconcileCommandState(ctx, cmd)
				continue
			}
			if cmd.IsTerminal() && run.Status == controlplane.RunAssigned {
				next := *run
				next.Status = controlplane.RunFailed
				next.Recoverable = true
				next.Err = "agent command interrupted before durable run claim"
				next.EndedAt = now
				if ok, err := s.store.CompareAndSwapRun(ctx, *run, next, false); err == nil && ok {
					s.jobs.cacheCommittedRun(&next)
				}
			}
			continue
		}
		if cmd.Status != controlplane.AgentCommandStatusAcknowledged && cmd.Status != controlplane.AgentCommandStatusRunning {
			continue
		}
		start := cmd.AckedAt
		if start.IsZero() {
			start = cmd.Delivered
		}
		if start.IsZero() {
			continue
		}
		deadline := start.Add(controlplane.AgentCommandExecutionLimit)
		if now.Before(deadline) {
			continue
		}
		// Complete the durable guest operation first. A late agent receipt cannot
		// change a terminal CAS; only then settle its command delivery record.
		if cmd.Execution != nil {
			svc := newGuestExecutionService(s.store, nil, func() time.Time { return now })
			op, err := svc.Get(ctx, cmd.Execution.ID)
			if err != nil {
				continue
			}
			if !guestExecutionTerminal(op.Status) {
				if _, err = svc.Complete(ctx, op.ID, controlplane.GuestExecutionResult{}, "execution_unconfirmed", "agent command execution window expired; outcome unknown"); err != nil {
					continue
				}
			}
		}
		if cmd.FilePut != nil {
			if err := newGuestFileOperationService(s.store).ReconcileCommandFailure(ctx, cmd.FilePut.ID, controlplane.AgentCommandStatusFailed); err != nil {
				continue
			}
		}
		expected := cmd
		cmd.Status = controlplane.AgentCommandStatusFailed
		cmd.Err = "agent command execution window expired; outcome unknown; manual recovery required"
		cmd.EndedAt = now
		_, _ = s.store.CompareAndSwapAgentCommand(ctx, expected, cmd)
	}
	if len(commands) < 128 {
		s.recoveryCursor = ""
	}
}
