package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

type ConsoleService struct {
	consoles       *consoleSessionHub
	scheduler      *SchedulerService
	publishCommand func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
	authorize      func(requestSubject, controlplane.ConsoleSession) error
	hclFile        func(string) string
	loadState      func(string) (*state.State, error)
	topologyHealth func(context.Context, string, *state.State) controlplane.TopologyHealth
	defaultTimeout time.Duration
	maxTimeout     time.Duration
}

type ConsoleCreateResult struct {
	Session controlplane.ConsoleSession
	Status  int
}

func newConsoleService(
	consoles *consoleSessionHub,
	scheduler *SchedulerService,
	publishCommand func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error),
	authorize func(requestSubject, controlplane.ConsoleSession) error,
	hclFile func(string) string,
	loadState func(string) (*state.State, error),
	topologyHealth func(context.Context, string, *state.State) controlplane.TopologyHealth,
	defaultTimeout time.Duration,
	maxTimeout time.Duration,
) *ConsoleService {
	return &ConsoleService{
		consoles:       consoles,
		scheduler:      scheduler,
		publishCommand: publishCommand,
		authorize:      authorize,
		hclFile:        hclFile,
		loadState:      loadState,
		topologyHealth: topologyHealth,
		defaultTimeout: defaultTimeout,
		maxTimeout:     maxTimeout,
	}
}

func (s *ConsoleService) CreateSession(ctx context.Context, topology, node string, req controlplane.ConsoleRequest, subj requestSubject) (ConsoleCreateResult, error) {
	if err := s.normalizeRequest(&req, subj); err != nil {
		return ConsoleCreateResult{Status: http.StatusBadRequest}, err
	}
	required, err := requiredCapabilitiesForNode(s.hclFile(topology), node)
	if err != nil {
		return ConsoleCreateResult{Status: http.StatusBadRequest}, err
	}
	agent, err := s.scheduler.SelectAgent(ctx, required, "")
	if err != nil {
		return ConsoleCreateResult{Status: http.StatusConflict}, err
	}
	sess := s.consoles.Create(topology, node, agent.ID, req)
	if err := s.authorize(requestSubject{User: req.RequestedBy, Roles: req.Roles}, sess); err != nil {
		s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
			sess.Status = controlplane.ConsoleSessionStatusDenied
			sess.Err = err.Error()
			sess.EndedAt = time.Now().UTC()
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "deny", err.Error()))
		})
		got, _ := s.consoles.Snapshot(sess.ID)
		return ConsoleCreateResult{Session: got, Status: http.StatusForbidden}, err
	}
	s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
		sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "allow", "console session allowed by policy"))
	})
	sess, _ = s.consoles.Snapshot(sess.ID)
	if err := s.ensureNodeRunning(ctx, topology, node); err != nil {
		s.consoles.Update(sess.ID, func(sess *controlplane.ConsoleSession) {
			sess.Status = controlplane.ConsoleSessionStatusFailed
			sess.Err = err.Error()
			sess.EndedAt = time.Now().UTC()
			sess.Audit = append(sess.Audit, consoleAuditEvent(*sess, "reject", err.Error()))
		})
		got, _ := s.consoles.Snapshot(sess.ID)
		return ConsoleCreateResult{Session: got, Status: http.StatusConflict}, err
	}
	if _, err := s.publishCommand(ctx, agent.ID, controlplane.AgentCommand{
		Type:    "session_open",
		Session: &sess,
		Request: req,
	}); err != nil {
		return ConsoleCreateResult{Status: http.StatusInternalServerError}, err
	}
	return ConsoleCreateResult{Session: sess, Status: http.StatusAccepted}, nil
}

func (s *ConsoleService) Cancel(ctx context.Context, id string, subj requestSubject) (controlplane.ConsoleSession, error) {
	if err := s.consoles.Cancel(id, "cancelled by api request", subj.User); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	sess, _ := s.consoles.Snapshot(id)
	_, _ = s.publishCommand(ctx, sess.AgentID, controlplane.AgentCommand{
		Type: "cancel_command",
		Session: &controlplane.ConsoleSession{
			ID: sess.ID,
		},
	})
	return sess, nil
}

func (s *ConsoleService) normalizeRequest(req *controlplane.ConsoleRequest, subj requestSubject) error {
	if req.TTY == nil {
		v := true
		req.TTY = &v
	}
	if req.Cols == 0 {
		req.Cols = 120
	}
	if req.Rows == 0 {
		req.Rows = 32
	}
	defaultTimeout := s.defaultTimeout
	maxTimeout := s.maxTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = time.Hour
	}
	if maxTimeout <= 0 {
		maxTimeout = 24 * time.Hour
	}
	if req.TimeoutSeconds == 0 && defaultTimeout > 0 {
		req.TimeoutSeconds = int(defaultTimeout.Seconds())
	}
	if req.TimeoutSeconds < 0 || time.Duration(req.TimeoutSeconds)*time.Second > maxTimeout {
		return fmt.Errorf("timeout_seconds must be between 0 and %d", int(maxTimeout.Seconds()))
	}
	if req.RequestedBy == "" {
		req.RequestedBy = subj.User
	}
	if len(req.Roles) == 0 {
		req.Roles = subj.Roles
	}
	req.Policy = "console.rbac"
	return nil
}

func (s *ConsoleService) ensureNodeRunning(ctx context.Context, topology, node string) error {
	st, err := s.loadState(topology)
	if err != nil {
		return err
	}
	health := s.topologyHealth(ctx, topology, st)
	resourceID := "sysbox_node." + node
	for _, resource := range health.Resources {
		if resource.Resource != resourceID {
			continue
		}
		if resource.Status != "healthy" {
			return fmt.Errorf("node %q is %s; repair required before opening a console", node, consoleHealthReason(resource))
		}
		if resource.Observation != nil && !resource.Observation.Running {
			return fmt.Errorf("node %q is %s; repair required before opening a console", node, consoleHealthReason(resource))
		}
		return nil
	}
	return fmt.Errorf("node %q has no health observation; repair required before opening a console", node)
}
