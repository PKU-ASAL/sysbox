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
	workspaces      *WorkspaceService
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
		workspaces:      server.workspaceService(),
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
	if s.store == nil {
		return completion, fmt.Errorf("run store not configured")
	}
	stored, changed, err := s.store.CompleteRun(context.Background(), agentID, runID, completion)
	if err != nil {
		return completion, err
	}
	completion.Run = *stored
	if s.jobs != nil {
		s.jobs.remember(stored)
		s.jobs.logs.Close(stored.ID)
	}
	if !changed {
		return completion, nil
	}
	if completion.Projection.AgentID == "" {
		completion.Projection.AgentID = agentID
	}
	if completion.Projection.Topology == "" {
		completion.Projection.Topology = stored.Topology
	}
	if completion.Projection.Workspace == "" {
		completion.Projection.Workspace = stored.Workspace
	}
	if completion.Projection.UpdatedAt.IsZero() {
		completion.Projection.UpdatedAt = time.Now().UTC()
	}
	if s.registry != nil {
		s.registry.SaveProjection(completion.Projection)
	}
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
		if cmd.Type == "cancel_command" && cmd.Session != nil {
			current, err := enqueueConsoleCancellation(ctx, s.store, cmd)
			if err != nil {
				return cmd, err
			}
			cmd = *current
		} else if err := s.store.SaveAgentCommand(ctx, cmd); err != nil {
			return cmd, err
		}
	}
	if s.registry != nil {
		return cmd, s.registry.PublishCommand(agentID, cmd)
	}
	return cmd, nil
}

// WakeCommand is only a delivery hint for an already committed outbox record.
func (s *AgentService) WakeCommand(agentID string, cmd controlplane.AgentCommand) error {
	if s.registry == nil {
		return nil
	}
	return s.registry.PublishCommand(agentID, cmd)
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

// RepublishAssignedRuns repairs a durable assignment whose command seed was
// lost before a controller restart. Running runs are left untouched: absence
// of a local process is not proof that a remote action stopped.
func (s *AgentService) RepublishAssignedRuns(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	runs, err := s.store.LoadRuns(ctx)
	if err != nil {
		return err
	}
	commands, err := s.store.ListAgentCommands(ctx, "")
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, cmd := range commands {
		if cmd.Run != nil {
			present[runCommandID(*cmd.Run)] = true
		}
	}
	for _, run := range latestRunsByID(runs) {
		if run.Status != controlplane.RunAssigned || run.AgentID == "" || present[runCommandID(run)] {
			continue
		}
		cmd, err := s.workspaces.runAssignedCommand(ctx, &run)
		if err != nil {
			return err
		}
		fingerprint := runAssignmentFingerprint(run)
		if _, _, err := s.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: run, Command: cmd, Fingerprint: fingerprint}); err != nil {
			return err
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
	case "run_assigned":
		if cmd.Run == nil {
			return "", time.Time{}, false
		}
		run, err := s.store.GetRun(ctx, cmd.Run.ID)
		if err != nil || run == nil {
			return "", time.Time{}, false
		}
		if !persistence.SameRunIncarnation(*run, *cmd.Run) {
			return controlplane.AgentCommandStatusCancelled, time.Now().UTC(), true
		}
		if run.Status.IsTerminal() {
			status := controlplane.AgentCommandStatusCompleted
			if run.Status != controlplane.RunDone {
				status = controlplane.AgentCommandStatusFailed
			}
			return status, run.EndedAt, true
		}
		return "", time.Time{}, false
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
	if err := s.updateCommandFromEvent(ctx, event); err != nil {
		slog.Warn("command event rejected", "command_id", event.CommandID, "error", err)
		return
	}
	if s.registry != nil {
		s.registry.SaveCommandEvent(event)
	}
	slog.Info("agent command event", "agent_id", event.AgentID, "command_id", event.CommandID, "type", event.Type, "status", event.Status)
}

func (s *AgentService) updateCommandFromEvent(ctx context.Context, event controlplane.AgentCommandEvent) error {
	if s.store == nil {
		return fmt.Errorf("agent command store not configured")
	}
	cmd, err := s.store.ApplyAgentCommandEvent(ctx, event)
	if err != nil {
		return err
	}
	if cmd.Type == "session_open" && cmd.IsTerminal() && cmd.Session != nil {
		return s.reconcileConsoleCompletion(ctx, cmd.Session.ID)
	}
	if err := s.updateConsoleSessionFromCommandEvent(*cmd, event); err != nil {
		return err
	}
	if cmd.Type == "node_operation" && failedDelivery(cmd.Status) {
		return reconcileUnissuedSSHFailure(ctx, s.store, *cmd)
	}
	if cmd.Type == "guest_file_put" && cmd.FilePut != nil && (cmd.Status == controlplane.AgentCommandStatusFailed || cmd.Status == controlplane.AgentCommandStatusDenied || cmd.Status == controlplane.AgentCommandStatusCancelled) {
		return newGuestFileOperationService(s.store).ReconcileCommandFailure(ctx, cmd.FilePut.ID, cmd.Status)
	}
	return nil
}

func (s *AgentService) updateConsoleSessionFromCommandEvent(cmd controlplane.AgentCommand, event controlplane.AgentCommandEvent) error {
	if cmd.Type != "session_open" || cmd.Session == nil || cmd.Session.ID == "" {
		return nil
	}
	if cmd.IsTerminal() && !cmd.EndedAt.IsZero() {
		// Replayed completion uses the original accepted command outcome,
		// not retry timestamps or unpersisted message text.
		event.Status, event.Error, event.Message, event.CreatedAt = cmd.Status, cmd.Err, "", cmd.EndedAt
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if s.consoles == nil {
		return fmt.Errorf("console session registry unavailable")
	}
	return s.consoles.Update(cmd.Session.ID, func(sess *controlplane.ConsoleSession) {
		if cmd.IsTerminal() && !cmd.EndedAt.IsZero() {
			sess.EndedAt = cmd.EndedAt
			sess.Err = cmd.Err
		}
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
