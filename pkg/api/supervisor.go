package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		policy:   supervisorPolicyFromEnv(),
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
	snap.Action = "restart_apply_started"
	snap.RunID = run.ID
	go s.server.runApply(topology, run)
}

func (s *Server) healthSnapshotFile(topology string) string {
	return filepath.Join(s.runsDir, topology, "health.json")
}

func (s *Server) saveHealthSnapshot(topology string, snap HealthSnapshot) error {
	path := s.healthSnapshotFile(topology)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (s *Server) loadHealthSnapshot(topology string) (*HealthSnapshot, error) {
	raw, err := os.ReadFile(s.healthSnapshotFile(topology))
	if err != nil {
		return nil, fmt.Errorf("health snapshot not found")
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode health snapshot: %w", err)
	}
	return &snap, nil
}

func supervisorPolicyFromEnv() SupervisorPolicy {
	switch os.Getenv("SYSBOX_SUPERVISOR_POLICY") {
	case string(SupervisorPolicyRestartOnCrash):
		return SupervisorPolicyRestartOnCrash
	default:
		return SupervisorPolicyObserveOnly
	}
}
