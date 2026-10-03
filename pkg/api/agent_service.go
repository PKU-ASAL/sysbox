package api

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type AgentService struct {
	store           apiStore
	registry        *agentRegistry
	jobs            *Jobs
	consoles        *consoleSessionHub
	commandTTL      func() time.Duration
	agentOfflineTTL func() time.Duration
	dispatchMu      sync.Mutex
	dispatchRetry   map[string]dispatchRetryState
	wakeup          func(string, controlplane.AgentCommand) error
}

type dispatchRetryState struct {
	failures  int
	next      time.Time
	lastError string
}

func newAgentService(server *Server) *AgentService {
	return &AgentService{
		store:           server.apiStore,
		registry:        server.agents,
		jobs:            server.jobs,
		consoles:        server.consoles,
		commandTTL:      server.cfg.AgentCommandTTL,
		agentOfflineTTL: server.cfg.AgentOfflineAfter,
		dispatchRetry:   map[string]dispatchRetryState{},
		wakeup:          server.agents.PublishCommand,
	}
}

func (s *AgentService) Save(ctx context.Context, agent controlplane.Agent) error {
	if s.store == nil {
		return fmt.Errorf("agent store not configured")
	}
	if err := s.store.SaveAgent(ctx, agent); err != nil {
		return err
	}
	// Cache only the normalized, persisted snapshot, never the submitted copy.
	_, err := s.Get(ctx, agent.ID)
	return err
}
func (s *AgentService) cacheAgent(agent *controlplane.Agent, err error) (*controlplane.Agent, error) {
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, persistence.ErrCorruptRecord
	}
	if s.registry != nil {
		s.registry.Save(*agent)
	}
	return agent, nil
}
func (s *AgentService) Get(ctx context.Context, id string) (*controlplane.Agent, error) {
	if s.store == nil {
		return nil, fmt.Errorf("agent store not configured")
	}
	return s.cacheAgent(s.store.GetAgent(ctx, id))
}
func (s *AgentService) List(ctx context.Context) ([]controlplane.Agent, error) {
	if s.store == nil {
		return nil, fmt.Errorf("agent store not configured")
	}
	return s.store.ListAgents(ctx)
}
func (s *AgentService) Register(ctx context.Context, report controlplane.Agent) (*controlplane.Agent, error) {
	if s.store == nil {
		return nil, fmt.Errorf("agent store not configured")
	}
	return s.cacheAgent(s.store.RegisterAgent(ctx, report))
}

func (s *AgentService) Heartbeat(ctx context.Context, report controlplane.Agent) (*controlplane.Agent, error) {
	if s.store == nil {
		return nil, fmt.Errorf("agent store not configured")
	}
	return s.cacheAgent(s.store.HeartbeatAgent(ctx, report))
}
func (s *AgentService) UpdatePolicy(ctx context.Context, id string, patch persistence.AgentPolicy) (*controlplane.Agent, error) {
	if s.store == nil {
		return nil, fmt.Errorf("agent store not configured")
	}
	return s.cacheAgent(s.store.UpdateAgentPolicy(ctx, id, patch))
}
func (s *AgentService) MarkStaleOffline(ctx context.Context, now time.Time) error {
	agents, err := s.List(ctx)
	if err != nil {
		return err
	}
	ttl := s.agentOfflineAfter()
	if ttl <= 0 {
		return nil
	}
	for _, agent := range agents {
		current, err := s.store.ExpireAgentHeartbeat(ctx, agent.ID, now, ttl)
		if err != nil {
			return err
		}
		if _, err := s.cacheAgent(current, nil); err != nil {
			return err
		}
	}
	return nil
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
	current, err := s.store.GetRun(context.Background(), runID)
	if err != nil {
		return completion, err
	}
	projection := completion.Projection
	if projection.AgentID != "" && projection.AgentID != agentID || projection.Topology != "" && projection.Topology != current.Topology || projection.Workspace != "" && projection.Workspace != current.Workspace {
		return completion, persistence.ErrConflict
	}
	incoming := completion.Run
	if current.AgentID != agentID || !incoming.Status.IsTerminal() {
		return completion, persistence.ErrConflict
	}
	if current.Attempt < 1 || incoming.Attempt != current.Attempt || incoming.LeaseOwner == "" || incoming.LeaseOwner != current.LeaseOwner {
		return completion, persistence.ErrConflict
	}
	if incoming.Topology != "" && incoming.Topology != current.Topology || incoming.Revision != "" && incoming.Revision != current.Revision || incoming.SnapshotPath != "" && incoming.SnapshotPath != current.SnapshotPath || incoming.Target != "" && incoming.Target != current.Target || incoming.Op != "" && incoming.Op != current.Op || incoming.Operation != "" && incoming.Operation != current.Operation || !incoming.QueuedAt.IsZero() && !incoming.QueuedAt.Equal(current.QueuedAt) {
		return completion, persistence.ErrConflict
	}
	merged := *current
	merged.Status = incoming.Status
	merged.Err = incoming.Err
	merged.Recoverable = incoming.Recoverable
	if !incoming.EndedAt.IsZero() {
		merged.EndedAt = incoming.EndedAt
	} else if merged.EndedAt.IsZero() {
		merged.EndedAt = time.Now().UTC()
	}
	if incoming.Assertion != nil {
		merged.Assertion = incoming.Assertion
	}
	completion.Run = merged
	if err := s.jobs.replace(&completion.Run); err != nil {
		return completion, err
	}
	if completion.Projection.AgentID == "" {
		completion.Projection.AgentID = agentID
	}
	if completion.Projection.Topology == "" {
		completion.Projection.Topology = completion.Run.Topology
	}
	if completion.Projection.Workspace == "" {
		completion.Projection.Workspace = completion.Run.Workspace
	}
	if completion.Projection.UpdatedAt.IsZero() {
		completion.Projection.UpdatedAt = time.Now().UTC()
	}
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

// RepublishPendingCommands rebuilds the in-memory wake-up hints from durable
// commands. The store remains authoritative; this only helps already connected
// streams notice work after a controller restart.
func (s *AgentService) RepublishPendingCommands(ctx context.Context) error {
	if s.store == nil || s.registry == nil {
		return nil
	}
	commands, err := s.store.ListAgentCommands(ctx, "")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, cmd := range commands {
		if cmd.IsTerminal() || !cmd.Leasable(now) {
			continue
		}
		s.dispatchMu.Lock()
		retry := s.dispatchRetry[cmd.ID]
		s.dispatchMu.Unlock()
		if retry.failures == 0 && s.store != nil {
			if persisted, err := s.store.LoadDispatchRetry(ctx, cmd.ID); err == nil {
				retry = dispatchRetryState{failures: persisted.Failures, next: persisted.Next, lastError: persisted.LastError}
				s.dispatchMu.Lock()
				s.dispatchRetry[cmd.ID] = retry
				s.dispatchMu.Unlock()
			}
		}
		blocked := now.Before(retry.next)
		if blocked {
			continue
		}
		wake := s.wakeup
		if wake == nil {
			wake = s.registry.PublishCommand
		}
		if err := wake(cmd.AgentID, cmd); err != nil {
			s.dispatchMu.Lock()
			retry.failures++
			if retry.failures > 6 {
				retry.failures = 6
			}
			retry.next = now.Add(time.Duration(1<<retry.failures) * time.Second)
			retry.lastError = err.Error()
			s.dispatchRetry[cmd.ID] = retry
			s.dispatchMu.Unlock()
			if s.store != nil {
				_ = s.store.SaveDispatchRetry(ctx, DispatchRetryRecord{CommandID: cmd.ID, Failures: retry.failures, Next: retry.next, LastError: retry.lastError})
			}
			continue
		}
		s.dispatchMu.Lock()
		delete(s.dispatchRetry, cmd.ID)
		s.dispatchMu.Unlock()
		if s.store != nil {
			_ = s.store.ClearDispatchRetry(ctx, cmd.ID)
		}
	}
	return nil
}

func (s *AgentService) DispatchRetryState(commandID string) (failures int, next time.Time, lastError string) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	state := s.dispatchRetry[commandID]
	return state.failures, state.next, state.lastError
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
	return cmd, false
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
	commands, err := s.store.ListAgentCommands(ctx, agentID)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	for _, cmd := range commands {
		if cmd.ID == commandID {
			return cmd, nil
		}
	}
	return controlplane.AgentCommand{}, fmt.Errorf("agent command not found")
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
	cmd.Status = status
	cmd.EndedAt = endedAt
	if cmd.EndedAt.IsZero() {
		cmd.EndedAt = time.Now().UTC()
	}
	_ = s.store.SaveAgentCommand(ctx, cmd)
	return cmd, true
}

func (s *AgentService) guestCommandTerminalState(ctx context.Context, cmd controlplane.AgentCommand) (string, time.Time, bool) {
	switch cmd.Type {
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
		s.updateConsoleSessionFromCommandEvent(cmd, event)
	case controlplane.AgentCommandStatusCompleted:
		cmd.Status = controlplane.AgentCommandStatusCompleted
		cmd.EndedAt = now
		s.updateConsoleSessionFromCommandEvent(cmd, event)
	case controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
		cmd.Status = event.Status
		cmd.Err = event.Error
		cmd.EndedAt = now
		s.updateConsoleSessionFromCommandEvent(cmd, event)
		if cmd.Type == "guest_file_put" && cmd.FilePut != nil {
			_ = newGuestFileOperationService(s.store).ReconcileCommandFailure(ctx, cmd.FilePut.ID, event.Status)
		}
	}
	if cmd.IsTerminal() {
		_ = s.store.DeleteAgentCommand(ctx, cmd.AgentID, cmd.ID)
	} else {
		_ = s.store.SaveAgentCommand(ctx, cmd)
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
