package api

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

type Supervisor struct {
	server   *Server
	interval time.Duration
	policy   SupervisorPolicy
	stop     chan struct{}
	once     sync.Once
}

type SupervisorPolicy string

const (
	SupervisorPolicyObserveOnly    SupervisorPolicy = "observe_only"
	SupervisorPolicyRestartOnCrash SupervisorPolicy = "restart_on_crash"
)

type HealthSnapshot struct {
	Topology  string                      `json:"topology"`
	Observed  time.Time                   `json:"observed_at"`
	Health    controlplane.TopologyHealth `json:"health"`
	Policy    SupervisorPolicy            `json:"policy"`
	AutoHeal  bool                        `json:"auto_heal"`
	Action    string                      `json:"action,omitempty"`
	RunID     string                      `json:"run_id,omitempty"`
	Recovered []string                    `json:"recovered_runs,omitempty"`
	LastError string                      `json:"last_error,omitempty"`
}

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
	s.server.agentService().MarkStaleOffline(ctx, now)
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
	st, err := s.server.workspaceService().LoadState(topology)
	if err != nil {
		return err
	}
	snap := HealthSnapshot{
		Topology: topology,
		Observed: time.Now().UTC(),
		Health:   runtime.EvaluateTopologyHealth(ctx, st),
		Policy:   s.policy,
		AutoHeal: s.policy != SupervisorPolicyObserveOnly,
	}
	s.maybeRepair(topology, &snap)
	s.reconcileRecoverable(ctx, topology, &snap)
	return s.server.saveHealthSnapshot(topology, snap)
}

// reconcileRecoverable closes the recovery loop for a topology: it finds runs
// that crashed after mutating the outside world but before recording the result
// in state, and reconciles their checkpoint journal so the orphaned objects are
// adopted back.
//
// Recovery used to happen only when someone called POST /v1/runs/{id}/recover
// by hand. The supervisor already walks every topology on an interval, so it is
// the natural place to look; reconcileCheckpointJournal is idempotent and
// guarded by CheckMutationSafety, so running it here is safe to repeat.
func (s *Supervisor) reconcileRecoverable(ctx context.Context, topology string, snap *HealthSnapshot) {
	runs := s.server.jobs.recoverableRuns(topology)
	if len(runs) == 0 {
		return
	}
	mgr, err := s.server.stateManager(topology)
	if err != nil {
		snap.LastError = fmt.Sprintf("reconcile: state manager: %v", err)
		return
	}
	for _, run := range runs {
		owner := fmt.Sprintf("sysbox-api:supervisor:%s", run.ID)
		report, err := reconcileCheckpointJournal(ctx, s.server.apiStore, topology, run.ID, mgr, owner)
		if err != nil {
			snap.LastError = fmt.Sprintf("reconcile run %s: %v", run.ID, err)
			continue
		}
		if report != nil && len(report.Recovered) > 0 {
			snap.Recovered = append(snap.Recovered, run.ID)
		}
	}
}

func (s *Supervisor) maybeRepair(topology string, snap *HealthSnapshot) {
	if s.policy != SupervisorPolicyRestartOnCrash {
		snap.Action = "observe"
		return
	}
	if snap.Health.Status != controlplane.ResourceHealthDrifted {
		snap.Action = "healthy"
		return
	}
	if s.server.jobs.hasRunning(topology) {
		snap.Action = "skipped_running_operation"
		return
	}
	run := s.server.jobs.start(topology, "apply")
	run.ParentID = "supervisor"
	s.server.jobs.persist(run)
	snap.Action = "restart_apply_started"
	snap.RunID = run.ID
	required, err := requiredCapabilitiesForTopology(s.server.workspaceService().HCLFile(topology))
	if err != nil {
		s.server.jobs.finish(run, err)
		snap.Action = "restart_apply_failed"
		return
	}
	if err := s.server.scheduling().DispatchRun(context.Background(), run, required); err != nil {
		snap.Action = "restart_apply_failed"
	}
}

func (s *Server) healthSnapshotFile(topology string) string {
	return filepath.Join(s.runsDir, topology, "health.json")
}

func (s *Server) saveHealthSnapshot(topology string, snap HealthSnapshot) error {
	return s.apiStore.SaveHealth(context.Background(), topology, snap)
}

func (s *Server) loadHealthSnapshot(topology string) (*HealthSnapshot, error) {
	return s.apiStore.LoadHealth(context.Background(), topology)
}

func supervisorPolicyFromConfig(raw string) SupervisorPolicy {
	switch raw {
	case string(SupervisorPolicyRestartOnCrash):
		return SupervisorPolicyRestartOnCrash
	default:
		return SupervisorPolicyObserveOnly
	}
}
