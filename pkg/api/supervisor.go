package api

import (
	"context"
	"path/filepath"
	"sync"
	"time"

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
	Topology  string                 `json:"topology"`
	Observed  time.Time              `json:"observed_at"`
	Health    runtime.TopologyHealth `json:"health"`
	Policy    SupervisorPolicy       `json:"policy"`
	AutoHeal  bool                   `json:"auto_heal"`
	Action    string                 `json:"action,omitempty"`
	RunID     string                 `json:"run_id,omitempty"`
	LastError string                 `json:"last_error,omitempty"`
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
	names, err := s.server.topologyNames(ctx)
	if err != nil {
		return
	}
	for _, name := range names {
		_ = s.ScanTopology(ctx, name)
	}
}

func (s *Supervisor) ScanTopology(ctx context.Context, topology string) error {
	st, err := s.server.loadState(topology)
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
	return s.server.saveHealthSnapshot(topology, snap)
}

func (s *Supervisor) maybeRepair(topology string, snap *HealthSnapshot) {
	if s.policy != SupervisorPolicyRestartOnCrash {
		snap.Action = "observe"
		return
	}
	if snap.Health.Status != runtime.ResourceHealthDrifted {
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
	required, err := requiredCapabilitiesForTopology(s.server.hclFile(topology))
	if err != nil {
		s.server.jobs.finish(run, err)
		snap.Action = "restart_apply_failed"
		return
	}
	if err := s.server.dispatchRun(context.Background(), run, required); err != nil {
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
