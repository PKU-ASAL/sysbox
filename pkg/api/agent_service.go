package api

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type AgentService struct {
	recoveryMu      sync.Mutex
	recoveryCursor  string
	store           apiStore
	registry        *agentRegistry
	jobs            *Jobs
	consoles        *consoleSessionHub
	commandTTL      func() time.Duration
	agentOfflineTTL func() time.Duration
}

func newAgentService(server *Server) *AgentService {
	return &AgentService{
		store:           server.apiStore,
		registry:        server.agents,
		jobs:            server.jobs,
		consoles:        server.consoles,
		commandTTL:      server.cfg.AgentCommandTTL,
		agentOfflineTTL: server.cfg.AgentOfflineAfter,
	}
}

func (s *AgentService) Save(ctx context.Context, agent controlplane.Agent) error {
	if agent.Protocol == "" {
		agent.Protocol = controlplane.AgentProtocolVersion
	}
	if s.store != nil {
		if err := s.store.SaveAgent(ctx, agent); err != nil {
			return err
		}
	}
	if s.registry != nil {
		s.registry.Save(agent)
	}
	return nil
}

func (s *AgentService) Get(ctx context.Context, id string) (*controlplane.Agent, error) {
	if s.store != nil {
		if agent, err := s.store.GetAgent(ctx, id); err == nil {
			if s.registry != nil {
				s.registry.Save(*agent)
			}
			return agent, nil
		}
	}
	if s.registry != nil {
		return s.registry.Get(id)
	}
	return nil, fmt.Errorf("agent not found")
}

func (s *AgentService) List(ctx context.Context) []controlplane.Agent {
	agents := []controlplane.Agent{}
	if s.store != nil {
		if stored, err := s.store.ListAgents(ctx); err == nil {
			agents = append(agents, stored...)
		}
	}
	if len(agents) == 0 && s.registry != nil {
		agents = append(agents, s.registry.List()...)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return agents
}

func (s *AgentService) MarkStaleOffline(ctx context.Context, now time.Time) {
	if s.store == nil {
		return
	}
	offlineAfter := s.agentOfflineAfter()
	for _, agent := range s.List(ctx) {
		if agent.Disabled || agent.Quarantined || agent.LastHeartbeat.IsZero() {
			continue
		}
		if agent.Status == controlplane.AgentStatusOffline || !agent.LastHeartbeat.Before(now.Add(-offlineAfter)) {
			continue
		}
		agent.Status = controlplane.AgentStatusOffline
		agent.Reason = "heartbeat stale"
		agent.UpdatedAt = now
		_ = s.Save(ctx, agent)
	}
}

func (s *AgentService) agentOfflineAfter() time.Duration {
	if s.agentOfflineTTL == nil {
		return 0
	}
	return s.agentOfflineTTL()
}

func (s *AgentService) CompleteRun(agentID, runID string, completion controlplane.RunCompletion) (controlplane.RunCompletion, error) {
	if completion.Run.ID == "" {
		completion.Run.ID = runID
	}
	if completion.Run.ID != runID {
		return completion, fmt.Errorf("completion run id mismatch")
	}
	if completion.Run.AgentID == "" {
		completion.Run.AgentID = agentID
	}
	if completion.Run.AgentID != agentID {
		return completion, fmt.Errorf("completion agent id mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return completion, err
	}
	if current.AgentID != agentID || current.LeaseOwner == "" || current.LeaseOwner != completion.Run.LeaseOwner || current.Attempt != completion.Run.Attempt {
		return completion, fmt.Errorf("completion lease ownership mismatch")
	}
	if (completion.Projection.AgentID != "" && completion.Projection.AgentID != agentID) || (completion.Projection.Topology != "" && completion.Projection.Topology != current.Topology) || (completion.Projection.Workspace != "" && completion.Projection.Workspace != current.Workspace) {
		return completion, fmt.Errorf("completion projection ownership mismatch")
	}
	if !completion.Run.Status.IsTerminal() {
		return completion, fmt.Errorf("completion requires terminal status")
	}
	if current.Status.IsTerminal() {
		if current.Status != completion.Run.Status || current.Err != completion.Run.Err {
			return completion, fmt.Errorf("conflicting terminal completion")
		}
		if current.CompletionProjection == nil {
			return completion, fmt.Errorf("terminal run has no agent completion receipt")
		}
		completion.Run = *current
		completion.Projection = *current.CompletionProjection
	} else {
		if !current.CanRenewLease(agentID, completion.Run.LeaseOwner, time.Now().UTC()) {
			return completion, fmt.Errorf("completion lease expired or inactive")
		}
		next := *current
		next.Status = completion.Run.Status
		next.Err = completion.Run.Err
		next.Recoverable = completion.Run.Recoverable
		next.Assertion = completion.Run.Assertion
		next.EndedAt = time.Now().UTC()
		next.Inputs = nil
		projection := completion.Projection
		projection.AgentID = agentID
		projection.Topology = current.Topology
		projection.Workspace = current.Workspace
		projection.UpdatedAt = next.EndedAt
		next.CompletionProjection = &projection
		ok, err := s.store.CompareAndSwapRun(ctx, *current, next, true)
		if err != nil {
			return completion, err
		}
		if !ok {
			return completion, fmt.Errorf("completion ownership changed")
		}
		completion.Run = next
		completion.Projection = *next.CompletionProjection
	}
	s.jobs.cacheCommittedRun(&completion.Run)
	s.registry.SaveProjection(completion.Projection)
	return completion, nil
}

func (s *AgentService) PublishCommand(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, error) {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	if cmd.AgentID == "" {
		cmd.AgentID = agentID
	}
	if cmd.Status == "" {
		cmd.Status = controlplane.AgentCommandStatusQueued
	}
	if cmd.Protocol == "" {
		cmd.Protocol = controlplane.AgentProtocolVersion
	}
	if cmd.CreatedAt.IsZero() {
		cmd.CreatedAt = time.Now().UTC()
	}
	if s.store != nil {
		if err := s.store.SaveAgentCommand(ctx, cmd); err != nil {
			return cmd, err
		}
	}
	if s.registry != nil {
		return cmd, s.registry.PublishCommand(agentID, cmd)
	}
	return cmd, nil
}

func (s *AgentService) AcquireCommandLease(ctx context.Context, agentID string, cmd controlplane.AgentCommand) (controlplane.AgentCommand, bool) {
	now := time.Now().UTC()
	if cmd.AgentID != agentID || !cmd.IsPending() || !cmd.Leasable(now) {
		return cmd, false
	}
	owner := fmt.Sprintf("%s:%d", agentID, now.UnixNano())
	ttl := s.agentCommandTTL()
	if s.store != nil {
		leased, ok, err := s.store.AcquireAgentCommandLease(ctx, agentID, cmd.ID, owner, ttl)
		if err != nil || !ok || leased == nil {
			return cmd, false
		}
		return *leased, true
	}
	cmd.MarkLeased(owner, ttl, now)
	return cmd, true
}

func (s *AgentService) agentCommandTTL() time.Duration {
	if s.commandTTL == nil {
		return time.Minute
	}
	return s.commandTTL()
}

func (s *AgentService) FindCommand(ctx context.Context, agentID, commandID string) (controlplane.AgentCommand, error) {
	if s.store == nil {
		return controlplane.AgentCommand{}, fmt.Errorf("agent command store not configured")
	}
	cmd, err := s.store.GetAgentCommand(ctx, agentID, commandID)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	return *cmd, nil
}

// ReconcileCommandState prevents a stale delivery record from replaying work
// whose durable guest operation is already terminal.
func (s *AgentService) ReconcileCommandState(ctx context.Context, cmd controlplane.AgentCommand) (controlplane.AgentCommand, bool) {
	if s == nil || s.store == nil || cmd.IsTerminal() {
		return cmd, cmd.IsTerminal()
	}
	status, endedAt, ok := s.guestCommandTerminalState(ctx, cmd)
	if !ok {
		return cmd, false
	}
	expected := cmd
	cmd.Status = status
	cmd.EndedAt = endedAt
	if cmd.EndedAt.IsZero() {
		cmd.EndedAt = time.Now().UTC()
	}
	ok, err := s.store.CompareAndSwapAgentCommand(ctx, expected, cmd)
	return cmd, err == nil && ok
}

func (s *AgentService) guestCommandTerminalState(ctx context.Context, cmd controlplane.AgentCommand) (string, time.Time, bool) {
	switch cmd.Type {
	case "run_assigned":
		if cmd.Run == nil {
			return "", time.Time{}, false
		}
		run, err := s.store.GetRun(ctx, cmd.Run.ID)
		if err != nil {
			return "", time.Time{}, false
		}
		switch run.Status {
		case controlplane.RunDone:
			return controlplane.AgentCommandStatusCompleted, run.EndedAt, true
		case controlplane.RunFailed:
			return controlplane.AgentCommandStatusFailed, run.EndedAt, true
		case controlplane.RunCancelled:
			return controlplane.AgentCommandStatusCancelled, run.EndedAt, true
		}
		return "", time.Time{}, false
	case "guest_execution":
		if cmd.Execution == nil || cmd.Execution.ID == "" {
			return "", time.Time{}, false
		}
		execution, err := s.store.GetGuestExecution(ctx, cmd.Execution.ID)
		if err != nil || execution == nil {
			return "", time.Time{}, false
		}
		return commandStatusFromGuestStatus(execution.Status, execution.EndedAt)
	case "guest_file_put":
		if cmd.FilePut == nil || cmd.FilePut.ID == "" {
			return "", time.Time{}, false
		}
		op, err := s.store.GetGuestFileOperation(ctx, cmd.FilePut.ID)
		if err != nil || op == nil {
			return "", time.Time{}, false
		}
		return commandStatusFromGuestStatus(op.Status, op.EndedAt)
	default:
		return "", time.Time{}, false
	}
}

func commandStatusFromGuestStatus(status string, endedAt time.Time) (string, time.Time, bool) {
	switch status {
	case controlplane.GuestExecutionCompleted:
		return controlplane.AgentCommandStatusCompleted, endedAt, true
	case controlplane.GuestExecutionFailed:
		return controlplane.AgentCommandStatusFailed, endedAt, true
	case controlplane.GuestExecutionCancelled:
		return controlplane.AgentCommandStatusCancelled, endedAt, true
	default:
		return "", time.Time{}, false
	}
}

func (s *AgentService) RecordCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	s.updateCommandFromEvent(ctx, event)
	if s.registry != nil {
		s.registry.SaveCommandEvent(event)
	}
	slog.Info("agent command event", "agent_id", event.AgentID, "command_id", event.CommandID, "type", event.Type, "status", event.Status)
}

func (s *AgentService) updateCommandFromEvent(ctx context.Context, event controlplane.AgentCommandEvent) {
	if s.store == nil || event.CommandID == "" || event.AgentID == "" {
		return
	}
	cmd, err := s.FindCommand(ctx, event.AgentID, event.CommandID)
	if err != nil {
		return
	}
	if cmd.LeaseOwner != "" && event.LeaseOwner != cmd.LeaseOwner {
		return
	}
	if cmd.Type == "run_assigned" && (controlplane.AgentCommand{Status: event.Status}).IsTerminal() {
		if cmd.Run != nil {
			run, err := s.store.GetRun(ctx, cmd.Run.ID)
			if err != nil {
				return
			}
			if run.Status.IsTerminal() {
				s.ReconcileCommandState(ctx, cmd)
				return
			}
			if run.Status == controlplane.RunRunning {
				return
			}
		}
	}
	if cmd.IsTerminal() {
		return
	}
	expected := cmd
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch event.Status {
	case "ack":
		cmd.Status = controlplane.AgentCommandStatusAcknowledged
		cmd.AckedAt = now
	case "started":
		cmd.Status = controlplane.AgentCommandStatusRunning
	case controlplane.AgentCommandStatusCompleted:
		cmd.Status = controlplane.AgentCommandStatusCompleted
		cmd.EndedAt = now
	case controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
		cmd.Status = event.Status
		cmd.Err = event.Error
		cmd.EndedAt = now

	}
	ok, err := s.store.CompareAndSwapAgentCommand(ctx, expected, cmd)
	if err != nil || !ok {
		return
	}
	s.updateConsoleSessionFromCommandEvent(cmd, event)
	if (event.Status == controlplane.AgentCommandStatusFailed || event.Status == controlplane.AgentCommandStatusDenied || event.Status == controlplane.AgentCommandStatusCancelled) && cmd.Type == "guest_file_put" && cmd.FilePut != nil {
		_ = newGuestFileOperationService(s.store).ReconcileCommandFailure(ctx, cmd.FilePut.ID, event.Status)
	}

}

func (s *AgentService) updateConsoleSessionFromCommandEvent(cmd controlplane.AgentCommand, event controlplane.AgentCommandEvent) {
	if cmd.Type != "session_open" || cmd.Session == nil || cmd.Session.ID == "" {
		return
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if s.consoles == nil {
		return
	}
	s.consoles.Update(cmd.Session.ID, func(sess *controlplane.ConsoleSession) {
		switch event.Status {
		case "started":
			if sess.Status == controlplane.ConsoleSessionStatusQueued {
				sess.Status = controlplane.ConsoleSessionStatusRunning
			}
			if sess.StartedAt.IsZero() {
				sess.StartedAt = now
			}
		case "completed":
			if sess.Status != controlplane.ConsoleSessionStatusFailed && sess.Status != controlplane.ConsoleSessionStatusCancelled {
				sess.Status = controlplane.ConsoleSessionStatusClosed
			}
			if sess.EndedAt.IsZero() {
				sess.EndedAt = now
			}
		case "failed", "denied", "cancelled":
			sess.Status = event.Status
			sess.Err = event.Error
			if sess.Err == "" {
				sess.Err = event.Message
			}
			sess.EndedAt = now
		}
	})
}

func deliverAgentCommand(cmd controlplane.AgentCommand) controlplane.AgentCommand {
	cmd.MarkDelivered(time.Now().UTC())
	return cmd
}

func commandTerminal(status string) bool {
	return controlplane.AgentCommand{Status: status}.IsTerminal()
}

// Projections are recoverable views of atomic completion receipts. Rebuilding
// them never trusts a retry caller's body or overwrites a newer receipt.
func (s *AgentService) restoreCompletionProjections(ctx context.Context) error {
	runs, err := s.store.LoadRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range latestRunsByID(runs) {
		if run.Status.IsTerminal() && run.CompletionProjection != nil {
			s.registry.SaveProjection(*run.CompletionProjection)
		}
	}
	return nil
}
