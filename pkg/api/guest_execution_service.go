package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

const guestExecutionMaxTimeout = 24 * 60 * 60

var guestEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	errInvalidGuestExecution  = errors.New("invalid guest execution")
	errGuestExecutionConflict = errors.New("guest execution conflict")
)

type GuestExecutionService struct {
	store   guestExecutionPersistence
	publish func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
	enqueue func(context.Context, controlplane.GuestExecution, controlplane.AgentCommand) (controlplane.GuestExecution, error)
	now     func() time.Time
}

func newGuestExecutionService(store guestExecutionPersistence, publish func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error), now func() time.Time) *GuestExecutionService {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &GuestExecutionService{store: store, publish: publish, now: now}
}

func newGuestExecutionServiceWithOutbox(store guestExecutionPersistence, publish func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error), enqueue func(context.Context, controlplane.GuestExecution, controlplane.AgentCommand) (controlplane.GuestExecution, error), now func() time.Time) *GuestExecutionService {
	service := newGuestExecutionService(store, publish, now)
	service.enqueue = enqueue
	return service
}

func newGuestExecutionServiceFromStore(store guestExecutionPersistence, publish func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error), now func() time.Time) *GuestExecutionService {
	service := newGuestExecutionService(store, publish, now)
	service.enqueue = func(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) (controlplane.GuestExecution, error) {
		if err := store.CreateGuestExecutionDispatch(ctx, execution, command); err != nil {
			return controlplane.GuestExecution{}, err
		}
		execution.CommandID = command.ID
		return execution, nil
	}
	return service
}

func validateGuestExecutionRequest(req controlplane.GuestExecutionRequest) error {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return fmt.Errorf("%w: argv is required", errInvalidGuestExecution)
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > guestExecutionMaxTimeout {
		return fmt.Errorf("%w: timeout_seconds must be between 0 and %d", errInvalidGuestExecution, guestExecutionMaxTimeout)
	}
	for name := range req.Environment {
		if !guestEnvironmentName.MatchString(name) {
			return fmt.Errorf("%w: invalid environment name %q", errInvalidGuestExecution, name)
		}
	}
	return nil
}

func (s *GuestExecutionService) Create(ctx context.Context, topology, node, agentID string, req controlplane.GuestExecutionRequest) (controlplane.GuestExecution, error) {
	if err := validateGuestExecutionRequest(req); err != nil {
		return controlplane.GuestExecution{}, err
	}
	execution := controlplane.GuestExecution{ID: uuid.NewString(), Version: 1, Topology: topology, Node: node, AgentID: agentID, Status: controlplane.GuestExecutionQueued, Request: req, CreatedAt: s.now()}
	commandID := ""
	if s.enqueue != nil {
		commandID = uuid.NewString()
		execution.CommandID = commandID
	}
	public := execution
	public.Request = controlplane.GuestExecutionRequest{}
	command := controlplane.AgentCommand{ID: commandID, AgentID: agentID, Type: "guest_execution", Execution: &public, ExecutionRequest: req}
	if id, ok := substrate.OperationIdentityFromContext(ctx); ok {
		if err := id.Validate(); err != nil {
			return controlplane.GuestExecution{}, err
		}
		command.ActionID = execution.ID
		command.ResourceUID = id.ResourceUID
		command.Generation = id.Generation
		command.LeaseEpoch = 1
	}
	if s.enqueue != nil {
		command.Status = controlplane.AgentCommandStatusQueued
		command.Protocol = controlplane.AgentProtocolVersion
		command.CreatedAt = execution.CreatedAt
	}
	if s.enqueue != nil {
		queued, err := s.enqueue(ctx, execution, command)
		if err != nil {
			return controlplane.GuestExecution{}, err
		}
		execution = queued
		if s.publish != nil {
			if _, err := s.publish(ctx, agentID, command); err != nil {
				// Durable enqueue is authoritative; notification is only a wake-up.
			}
		}
		return execution, nil
	}
	if err := s.store.SaveGuestExecution(ctx, execution); err != nil {
		return controlplane.GuestExecution{}, err
	}
	if s.publish != nil {
		command, err := s.publish(ctx, agentID, command)
		if err != nil {
			execution.Status = controlplane.GuestExecutionFailed
			execution.ResultClass = "dispatch"
			execution.Err = "dispatch failed"
			execution.EndedAt = s.now()
			execution.ExpiresAt = execution.EndedAt.Add(15 * time.Minute)
			ok, saveErr := s.store.CompareAndSwapGuestExecution(ctx, execution, execution.Version)
			if saveErr != nil {
				return controlplane.GuestExecution{}, fmt.Errorf("record dispatch failure: %w", saveErr)
			}
			if !ok {
				return controlplane.GuestExecution{}, fmt.Errorf("%w: dispatch failure update", errGuestExecutionConflict)
			}
			return controlplane.GuestExecution{}, fmt.Errorf("publish guest execution: %w", err)
		}
		execution.CommandID = command.ID
		if ok, saveErr := s.store.CompareAndSwapGuestExecution(ctx, execution, execution.Version); saveErr != nil || !ok {
			return controlplane.GuestExecution{}, fmt.Errorf("record guest execution command")
		} else {
			execution.Version++
		}
	}
	return execution, nil
}

func (s *GuestExecutionService) Get(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	execution, err := s.store.GetGuestExecution(ctx, id)
	if err != nil {
		return controlplane.GuestExecution{}, err
	}
	if !execution.ExpiresAt.IsZero() && !s.now().Before(execution.ExpiresAt) && (execution.Request.Argv != nil || execution.Result.Stdout != "" || execution.Result.Stderr != "") {
		execution.Request = controlplane.GuestExecutionRequest{}
		execution.Result.Stdout = ""
		execution.Result.Stderr = ""
		if ok, casErr := s.store.CompareAndSwapGuestExecution(ctx, *execution, execution.Version); casErr == nil && ok {
			execution.Version++
		}
	}
	return *execution, nil
}
func guestExecutionTerminal(status string) bool {
	return status == controlplane.GuestExecutionCompleted || status == controlplane.GuestExecutionFailed || status == controlplane.GuestExecutionCancelled
}
func (s *GuestExecutionService) MarkRunning(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	execution, err := s.Get(ctx, id)
	if err != nil {
		return execution, err
	}
	if execution.Status != controlplane.GuestExecutionQueued {
		return execution, fmt.Errorf("%w: cannot start from %s", errGuestExecutionConflict, execution.Status)
	}
	execution.Status = controlplane.GuestExecutionRunning
	execution.StartedAt = s.now()
	ok, err := s.store.CompareAndSwapGuestExecution(ctx, execution, execution.Version)
	if err == nil && !ok {
		err = fmt.Errorf("%w: version changed", errGuestExecutionConflict)
	} else if ok {
		execution.Version++
	}
	return execution, err
}
func (s *GuestExecutionService) Cancel(ctx context.Context, id string) (controlplane.GuestExecution, error) {
	execution, err := s.Get(ctx, id)
	if err != nil {
		return execution, err
	}
	if guestExecutionTerminal(execution.Status) {
		return execution, fmt.Errorf("%w: already terminal", errGuestExecutionConflict)
	}
	execution.Status = controlplane.GuestExecutionCancelled
	execution.EndedAt = s.now()
	execution.ExpiresAt = execution.EndedAt.Add(15 * time.Minute)
	ok, err := s.store.CompareAndSwapGuestExecution(ctx, execution, execution.Version)
	if err == nil && !ok {
		err = fmt.Errorf("%w: version changed", errGuestExecutionConflict)
	} else if ok {
		execution.Version++
	}
	if err == nil && s.publish != nil {
		target := execution.CommandID
		if execution.StartedAt.IsZero() && target != "" {
			if commands, ok := s.store.(interface {
				DeleteAgentCommand(context.Context, string, string) error
			}); ok {
				err = commands.DeleteAgentCommand(ctx, execution.AgentID, target)
			} else {
				err = fmt.Errorf("command retirement storage required")
			}
		} else {
			_, err = s.publish(ctx, execution.AgentID, controlplane.AgentCommand{Type: "cancel_command", Operation: controlplane.NodeOperation{ExternalID: execution.ID}})
		}
		if err != nil {
			return execution, fmt.Errorf("publish guest execution cancel: %w", err)
		}
	}
	return execution, err
}
