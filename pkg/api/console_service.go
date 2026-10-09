package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
)

type ConsoleService struct {
	consoles       *consoleSessionHub
	scheduler      *SchedulerService
	publishCommand func(context.Context, string, controlplane.AgentCommand) (controlplane.AgentCommand, error)
	wakeCommand    func(string, controlplane.AgentCommand) error
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
	wakeCommand func(string, controlplane.AgentCommand) error,
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
		wakeCommand:    wakeCommand,
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
	agent, err := s.scheduler.SelectAgentForTopology(ctx, topology, required, "")
	if err != nil {
		return ConsoleCreateResult{Status: http.StatusConflict}, err
	}
	sess := newConsoleSession(topology, node, agent.ID, req)
	if err := s.authorize(subj, sess); err != nil {
		sess.Status = controlplane.ConsoleSessionStatusDenied
		sess.Err = err.Error()
		sess.EndedAt = time.Now().UTC()
		sess.Audit = append(sess.Audit, consoleAuditEvent(sess, "deny", err.Error()))
		if _, saveErr := s.consoles.persistSession(ctx, sess, req, nil); saveErr != nil {
			return ConsoleCreateResult{Status: http.StatusServiceUnavailable}, saveErr
		}
		return ConsoleCreateResult{Session: sess, Status: http.StatusForbidden}, err
	}
	sess.Audit = append(sess.Audit, consoleAuditEvent(sess, "allow", "console session allowed by policy"))
	if err := s.ensureNodeRunning(ctx, topology, node); err != nil {
		sess.Status = controlplane.ConsoleSessionStatusFailed
		sess.Err = err.Error()
		sess.EndedAt = time.Now().UTC()
		sess.Audit = append(sess.Audit, consoleAuditEvent(sess, "reject", err.Error()))
		if _, saveErr := s.consoles.persistSession(ctx, sess, req, nil); saveErr != nil {
			return ConsoleCreateResult{Status: http.StatusServiceUnavailable}, saveErr
		}
		return ConsoleCreateResult{Session: sess, Status: http.StatusConflict}, err
	}
	cmd := persistence.DurableCommand(controlplane.AgentCommand{
		ID: "console-open-" + sess.ID, AgentID: agent.ID, Type: "session_open",
		Session: &sess, Request: req, CreatedAt: sess.CreatedAt,
	})
	if _, err := s.consoles.persistSession(ctx, sess, req, &cmd); err != nil {
		return ConsoleCreateResult{Status: http.StatusServiceUnavailable}, err
	}
	if s.wakeCommand != nil {
		if err := s.wakeCommand(agent.ID, cmd); err != nil {
			slog.Warn("console outbox wake failed; durable dispatch will retry", "session_id", sess.ID, "error", err)
		}
	}
	return ConsoleCreateResult{Session: sess, Status: http.StatusAccepted}, nil
}

func (s *ConsoleService) Cancel(ctx context.Context, id string, subj requestSubject) (controlplane.ConsoleSession, error) {
	if err := s.consoles.Cancel(id, "cancelled by api request", subj.User); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	sess, err := s.consoles.Snapshot(id)
	if err != nil {
		return controlplane.ConsoleSession{}, err
	}
	_, err = s.publishCommand(ctx, sess.AgentID, consoleCancellationCommand(sess))
	if err != nil {
		return sess, fmt.Errorf("publish console cancellation: %w", err)
	}
	return sess, nil
}

// Immutable session identity determines a single durable cancellation command.
// Status/audit fields change on retry and must never become command identity.
func consoleCancellationCommand(sess controlplane.ConsoleSession) controlplane.AgentCommand {
	identity := controlplane.ConsoleSession{ID: sess.ID, AgentID: sess.AgentID, Topology: sess.Topology, Node: sess.Node, CreatedAt: sess.CreatedAt}
	raw, _ := json.Marshal(identity)
	id := fmt.Sprintf("console-cancel-%x", sha256.Sum256(raw))
	return controlplane.AgentCommand{ID: id, AgentID: sess.AgentID, Type: "cancel_command", Session: &identity}
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
	// Identity and roles come from the server's request subject, never from the
	// console request body. Persist the same identity that policy evaluated.
	req.RequestedBy = subj.User
	req.Roles = append([]string(nil), subj.Roles...)
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
