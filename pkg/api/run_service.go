package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

var operationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)

func validateOperationKey(key string) error {
	if !operationKeyPattern.MatchString(key) {
		return fmt.Errorf("operation key must be 1-128 ASCII letters, digits, colon, underscore, or hyphen")
	}
	return nil
}

type RunService struct {
	jobs            *Jobs
	plans           *PlanService
	scheduler       *SchedulerService
	hclFile         func(string) string
	stateManager    func(string) (*state.Manager, error)
	requiredForTopo func(string) ([]string, error)
}

type RunStartRequest struct {
	PlanID           string
	Revision         string
	AgentID          string
	Target           string
	AllowUnsafeState bool
}

type runServiceErrorKind string

const (
	runServiceBadRequest runServiceErrorKind = "bad_request"
	runServiceConflict   runServiceErrorKind = "conflict"
	runServiceNotFound   runServiceErrorKind = "not_found"
	runServiceInternal   runServiceErrorKind = "internal"
)

type runServiceError struct {
	kind runServiceErrorKind
	err  error
}

func (e runServiceError) Error() string {
	if e.err == nil {
		return string(e.kind)
	}
	return e.err.Error()
}

func (e runServiceError) Unwrap() error { return e.err }

func newRunService(server *Server) *RunService {
	return &RunService{
		jobs:            server.jobs,
		plans:           server.plans(),
		scheduler:       server.scheduling(),
		hclFile:         server.workspaceService().HCLFile,
		stateManager:    server.stateManager,
		requiredForTopo: requiredCapabilitiesForTopology,
	}
}

func runError(kind runServiceErrorKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return runServiceError{kind: kind, err: err}
}

func runServiceStatus(err error) int {
	var svcErr runServiceError
	if !errors.As(err, &svcErr) {
		return http.StatusInternalServerError
	}
	switch svcErr.kind {
	case runServiceBadRequest:
		return http.StatusBadRequest
	case runServiceConflict:
		return http.StatusConflict
	case runServiceNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func (s *RunService) StartApply(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	if req.PlanID != "" {
		currentSerial, err := s.currentStateSerial(ctx, topology)
		if err != nil {
			return nil, runError(runServiceInternal, err)
		}
		plan, err := s.ValidateStoredPlanForApply(ctx, topology, req.PlanID, currentSerial)
		if err != nil {
			return nil, runError(runServiceBadRequest, err)
		}
		req.Revision = plan.Revision
	}
	run := s.jobs.startWithOptions(topology, "apply", runStartOptions{
		Revision:    req.Revision,
		PlanID:      req.PlanID,
		AgentID:     req.AgentID,
		UnsafeState: req.AllowUnsafeState,
	})
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) ValidateStoredPlanForApply(ctx context.Context, topology, planID string, currentSerial int64) (*controlplane.Plan, error) {
	return s.plans.ValidateStoredPlanForApply(ctx, topology, planID, currentSerial)
}

func (s *RunService) StartRepair(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	run := s.jobs.startWithOptions(topology, "repair", runStartOptions{
		Revision:    req.Revision,
		AgentID:     req.AgentID,
		UnsafeState: req.AllowUnsafeState,
	})
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) StartReset(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	run := s.jobs.startWithOptions(topology, "reset", runStartOptions{
		Revision: req.Revision, AgentID: req.AgentID, Target: req.Target, UnsafeState: req.AllowUnsafeState,
	})
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) StartDestroy(ctx context.Context, topology string) (*controlplane.Run, error) {
	return s.StartDestroyWithOptions(ctx, topology, false)
}

func (s *RunService) StartDestroyWithOptions(ctx context.Context, topology string, allowUnsafe bool) (*controlplane.Run, error) {
	return s.startDestroy(ctx, topology, allowUnsafe, "")
}

func (s *RunService) StartDestroyWithOperationKey(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	if err := validateOperationKey(operationKey); err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	return s.startDestroy(ctx, topology, allowUnsafe, operationKey)
}

func (s *RunService) startDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	if operationKey != "" {
		return s.startIdempotentDestroy(ctx, topology, allowUnsafe, operationKey)
	}
	run, created := s.jobs.startWithResult(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	if !created {
		return run, nil
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *RunService) startIdempotentDestroy(ctx context.Context, topology string, allowUnsafe bool, operationKey string) (*controlplane.Run, error) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("topology=%s\noperation=destroy\nallow_unsafe_state=%t\n", topology, allowUnsafe))))
	run := newRun(topology, "destroy", runStartOptions{UnsafeState: allowUnsafe, OperationKey: operationKey})
	run.RequestFingerprint = fingerprint
	existing, found, err := s.jobs.store.GetRunDispatch(ctx, run.ID, fingerprint)
	if err != nil {
		if errors.Is(err, errIdempotencyConflict) {
			return nil, runError(runServiceConflict, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	if found {
		s.jobs.remember(existing)
		return existing, nil
	}
	required, err := s.requiredForTopo(s.hclFile(topology))
	if err != nil {
		return nil, runError(runServiceBadRequest, err)
	}
	agent, err := s.scheduler.SelectAgent(ctx, required, "")
	if err != nil {
		return nil, runError(runServiceConflict, err)
	}
	run.MarkAssigned(agent.ID, time.Now().UTC())
	command := controlplaneRunAssignedCommand(run)
	command.ID = "run-" + run.ID
	command.AgentID = agent.ID
	command.Status = controlplane.AgentCommandStatusQueued
	command.Protocol = controlplane.AgentProtocolVersion
	command.CreatedAt = time.Now().UTC()
	stored, created, err := s.jobs.store.CreateRunDispatch(ctx, RunDispatchRequest{Run: *run, Command: command, Fingerprint: fingerprint})
	if err != nil {
		if errors.Is(err, errIdempotencyConflict) {
			return nil, runError(runServiceConflict, err)
		}
		return nil, runError(runServiceInternal, err)
	}
	s.jobs.remember(stored)
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	return stored, nil
}

func (s *RunService) Resume(ctx context.Context, runID string) (*controlplane.Run, *controlplane.Run, error) {
	parent, ok := s.jobs.get(runID)
	if !ok {
		return nil, nil, runError(runServiceNotFound, fmt.Errorf("run not found"))
	}
	if parent.Status == controlplane.RunRunning {
		return nil, parent, runError(runServiceConflict, fmt.Errorf("run %s is still running", runID))
	}
	if parent.Op != "apply" && parent.Op != "destroy" && parent.Op != "reset" {
		return nil, parent, runError(runServiceBadRequest, fmt.Errorf("run op %q cannot be resumed", parent.Op))
	}
	run := s.jobs.startChild(parent)
	if err := s.dispatchTopologyRun(ctx, run, run.Topology); err != nil {
		return nil, parent, err
	}
	return run, parent, nil
}

func (s *RunService) DispatchRun(ctx context.Context, run *controlplane.Run, required []string) error {
	return s.scheduler.DispatchRun(ctx, run, required)
}

func (s *RunService) dispatchTopologyRun(ctx context.Context, run *controlplane.Run, topology string) error {
	required, err := s.requiredForTopo(s.hclFile(topology))
	if err != nil {
		s.jobs.finish(run, err)
		return runError(runServiceBadRequest, err)
	}
	if err := s.DispatchRun(ctx, run, required); err != nil {
		return runError(runServiceConflict, err)
	}
	return nil
}

func (s *RunService) currentStateSerial(ctx context.Context, topology string) (int64, error) {
	mgr, err := s.stateManager(topology)
	if err != nil {
		return 0, err
	}
	meta, err := mgr.Metadata(ctx)
	if err != nil {
		return 0, err
	}
	return meta.Serial, nil
}
