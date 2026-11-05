package api

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
)

type Supervisor struct {
	server   *Server
	interval time.Duration
	policy   controlstore.SupervisorPolicy
	stop     chan struct{}
	once     sync.Once
}

const (
	SupervisorPolicyObserveOnly    controlstore.SupervisorPolicy = "observe_only"
	SupervisorPolicyRestartOnCrash controlstore.SupervisorPolicy = "restart_on_crash"
)

func newSupervisor(s *Server, interval time.Duration) *Supervisor {
	return &Supervisor{
		server:   s,
		interval: interval,
		policy:   supervisorPolicyFromConfig(s.cfg.Supervisor.Policy),
		stop:     make(chan struct{}),
	}
}

func (s *Supervisor) Start() {
	if s == nil || s.interval <= 0 {
		return
	}
	go s.loop()
}

func (s *Supervisor) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.stop)
	})
}

func (s *Supervisor) loop() {
	s.Scan(context.Background())
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.Scan(context.Background())
		case <-s.stop:
			return
		}
	}
}

func (s *Supervisor) Scan(ctx context.Context) {
	now := time.Now().UTC()
	if err := s.server.workspaceService().RecoverDeletionIntents(ctx); err != nil {
		slog.Warn("deletion intent recovery failed", "error", err)
	}
	if err := s.server.agentService().reconcileConsoleCompletions(ctx); err != nil {
		slog.Warn("console completion reconciliation failed", "error", err)
	}
	if err := s.server.agentService().reconcileConsoleCancellations(ctx); err != nil {
		slog.Warn("console cancellation reconciliation failed", "error", err)
	}
	if err := s.server.agentService().reconcileGuestExecutionCancellations(ctx); err != nil {
		slog.Warn("guest execution cancellation reconciliation failed", "error", err)
	}
	if err := s.server.agentService().reconcileGuestHostCompletions(ctx); err != nil {
		slog.Warn("guest host completion reconciliation failed", "error", err)
	}
	if err := s.server.agentService().reconcileUnissuedNodeFailures(ctx); err != nil {
		slog.Warn("unissued SSH failure reconciliation failed", "error", err)
	}
	if err := s.server.agentService().reconcileUnissuedGuestFailures(ctx); err != nil {
		slog.Warn("unissued guest failure reconciliation failed", "error", err)
	}
	if err := s.server.agentService().RepublishPendingCommands(ctx); err != nil {
		fmt.Printf("[supervisor] republish pending commands: %v\n", err)
	}
	if err := s.server.agentService().RepublishAssignedRuns(ctx); err != nil {
		fmt.Printf("[supervisor] repair assigned run commands: %v\n", err)
	}
	if err := s.server.agentService().MarkStaleOffline(ctx, now); err != nil {
		fmt.Printf("[supervisor] update agent liveness: %v\n", err)
		return
	}
	s.server.jobs.markExpiredLeases(now)
	s.server.jobs.markConvergenceDeadlineExceeded(now, s.server.cfg.ConvergeTimeout())
	names, err := s.server.workspaceService().Names(ctx)
	if err != nil {
		return
	}
	for _, name := range names {
		if err := s.ScanTopology(ctx, name); err != nil {
			// A failed scan means no health snapshot is written for this
			// topology, so Provisioned stays Unknown. Log the reason instead of
			// swallowing it: an empty health table plus an empty log is the
			// hardest failure to diagnose.
			slog.Warn("supervisor scan topology failed", "topology", name, "error", err)
		}
	}
}

func (s *Supervisor) ScanTopology(ctx context.Context, topology string) error {
	if err := s.server.reconcileGCOutbox(ctx, topology); err != nil {
		return err
	}
	if err := s.server.finishCommittedRestores(ctx, topology); err != nil {
		return err
	}
	st, err := s.server.workspaceService().LoadState(topology)
	if err != nil {
		// The first create may have durable intent but no committed resource yet.
		// That aggregate still needs observation after a crash.
		repository, repositoryErr := s.server.actionRepository(topology)
		if repositoryErr != nil {
			return repositoryErr
		}
		actions, actionsErr := repository.LoadActions(ctx, topology)
		if actionsErr != nil {
			return actionsErr
		}
		if len(actions) == 0 {
			return err
		}
		st, err = repository.LoadState(ctx, topology)
		if err != nil {
			return err
		}
	}
	snap := controlstore.HealthSnapshot{
		Topology: topology,
		Observed: time.Now().UTC(),
		Health:   s.server.authoritativeTopologyHealth(ctx, topology, st),
		Policy:   s.policy,
		AutoHeal: false,
	}
	s.maybeRepair(topology, &snap)
	return s.server.saveHealthSnapshot(topology, snap)
}

func (s *Supervisor) maybeRepair(topology string, snap *controlstore.HealthSnapshot) {
	if s.policy != SupervisorPolicyRestartOnCrash {
		snap.Action = "observe"
		return
	}
	if snap.Health.Status != model.ResourceHealthDrifted {
		snap.Action = "healthy"
		return
	}
	if s.server.jobs.hasRunning(topology) {
		snap.Action = "skipped_running_operation"
		return
	}
	// A broad historical restart flag is not authorization for replacement,
	// destruction, or arbitrary guest setup. A bounded action plan is required.
	snap.Action = "approval_required"
	snap.LastError = "runtime drift requires an explicitly approved action plan"
}

func (s *Server) healthSnapshotFile(topology string) string {
	return filepath.Join(s.runsDir, topology, "health.json")
}

func (s *Server) saveHealthSnapshot(topology string, snap controlstore.HealthSnapshot) error {
	return s.apiStore.SaveHealth(context.Background(), topology, snap)
}

func (s *Server) loadHealthSnapshot(topology string) (*controlstore.HealthSnapshot, error) {
	return s.apiStore.LoadHealth(context.Background(), topology)
}

func supervisorPolicyFromConfig(raw string) controlstore.SupervisorPolicy {
	switch raw {
	case string(SupervisorPolicyRestartOnCrash):
		return SupervisorPolicyRestartOnCrash
	default:
		return SupervisorPolicyObserveOnly
	}
}
