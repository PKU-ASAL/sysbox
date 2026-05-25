package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const (
	DefaultAgentID = "local"
)

type Run = controlplane.Run
type RunStatus = controlplane.RunStatus

const (
	RunQueued    = controlplane.RunQueued
	RunAssigned  = controlplane.RunAssigned
	RunRunning   = controlplane.RunRunning
	RunDone      = controlplane.RunDone
	RunFailed    = controlplane.RunFailed
	RunCancelled = controlplane.RunCancelled
)

// Run represents one async apply or destroy operation.
// Fields are JSON-serialisable so they can be persisted by the API store.
// Jobs is a run store backed by in-memory map + the API persistence store.
// Per-topology mutexes prevent concurrent apply/destroy on the same topology.
type Jobs struct {
	mu                 sync.RWMutex
	runs               map[string]*Run
	runsDir            string // root directory for runs, e.g. "runs"
	store              apiStore
	recoverInterrupted bool
	logs               map[string]*Broadcaster

	topologyMu    sync.Mutex
	topologyLocks map[string]*sync.Mutex
}

type runStartOptions struct {
	ParentID string
	Revision string
	PlanID   string
	AgentID  string
}

func newJobs(runsDir string, store apiStore) *Jobs {
	return newJobsWithRecovery(runsDir, store, true)
}

func newJobsWithRecovery(runsDir string, store apiStore, recoverInterrupted bool) *Jobs {
	if store == nil {
		store = &localAPIStore{runsDir: runsDir}
	}
	j := &Jobs{
		runs:               make(map[string]*Run),
		runsDir:            runsDir,
		store:              store,
		recoverInterrupted: recoverInterrupted,
		logs:               make(map[string]*Broadcaster),
		topologyLocks:      make(map[string]*sync.Mutex),
	}
	j.load()
	return j
}

// lockTopology acquires a per-topology mutex. Returns a function to unlock.
// This ensures that concurrent apply/destroy requests for the same topology
// are serialised, preventing state file corruption and double-create bugs.
func (j *Jobs) lockTopology(topology string) func() {
	j.topologyMu.Lock()
	mu, ok := j.topologyLocks[topology]
	if !ok {
		mu = &sync.Mutex{}
		j.topologyLocks[topology] = mu
	}
	j.topologyMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// load populates the in-memory store from persisted run records.
func (j *Jobs) load() {
	runs, err := j.store.LoadRuns(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[api] load runs: %v\n", err)
		return
	}
	if j.recoverInterrupted {
		runs = markInterruptedRuns(runs)
	}
	for _, r := range runs {
		run := r
		normalizeRunProductFields(&run)
		j.ensureLogs(run.ID, true)
		j.runs[run.ID] = &run
	}
	j.loadCheckpoints()
}

func (j *Jobs) loadCheckpoints() {
	pattern := filepath.Join(j.runsDir, "*", "runs", "*.checkpoint.json")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		id := filepath.Base(f)
		id = id[:len(id)-len(".checkpoint.json")]
		if _, ok := j.runs[id]; ok {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var cp struct {
			RunID     string    `json:"run_id"`
			Topology  string    `json:"topology"`
			Operation string    `json:"operation"`
			Status    string    `json:"status"`
			StartedAt time.Time `json:"started_at"`
			EndedAt   time.Time `json:"ended_at"`
		}
		if err := json.Unmarshal(data, &cp); err != nil || cp.RunID == "" {
			continue
		}
		status := RunFailed
		errMsg := "server restarted before run completion"
		if cp.Status == "done" {
			status = RunDone
			errMsg = ""
		}
		r := &Run{
			ID:          cp.RunID,
			ProjectID:   "default",
			Workspace:   cp.Topology,
			Topology:    cp.Topology,
			Op:          cp.Operation,
			Status:      status,
			Err:         errMsg,
			Recoverable: status == RunFailed,
			StartedAt:   cp.StartedAt,
			EndedAt:     cp.EndedAt,
		}
		j.ensureLogs(r.ID, true)
		j.runs[r.ID] = r
	}
}

// persist writes a run record through the configured API store.
func (j *Jobs) persist(r *Run) {
	if err := j.store.SaveRun(context.Background(), runRecord(*r)); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: %v\n", err)
	}
}

func runRecord(r Run) Run {
	normalizeRunProductFields(&r)
	return r
}

func normalizeRunProductFields(r *Run) {
	if r == nil {
		return
	}
	if r.ProjectID == "" {
		r.ProjectID = "default"
	}
	if r.Workspace == "" {
		r.Workspace = r.Topology
	}
	if r.AgentID == "" {
		r.AgentID = DefaultAgentID
	}
}

func (j *Jobs) start(topology, op string) *Run {
	return j.startWithOptions(topology, op, runStartOptions{})
}

func (j *Jobs) startWithOptions(topology, op string, opts runStartOptions) *Run {
	now := time.Now()
	r := &Run{
		ID:         uuid.New().String(),
		ProjectID:  "default",
		Workspace:  topology,
		Topology:   topology,
		Op:         op,
		Status:     RunQueued,
		ParentID:   opts.ParentID,
		Revision:   opts.Revision,
		PlanID:     opts.PlanID,
		AgentID:    opts.AgentID,
		LeaseOwner: "sysbox-api",
		QueuedAt:   now,
		StartedAt:  now,
	}
	normalizeRunProductFields(r)
	r.LeaseOwner = fmt.Sprintf("sysbox-api:%s:%s", r.Op, r.ID)
	j.mu.Lock()
	j.runs[r.ID] = r
	j.ensureLogsLocked(r.ID, false)
	j.mu.Unlock()
	j.persist(r)
	return r
}

func (j *Jobs) assign(r *Run, agentID string) {
	j.mu.Lock()
	r.AgentID = agentID
	r.Status = RunAssigned
	r.AssignedAt = time.Now()
	j.mu.Unlock()
	j.persist(r)
}

func (j *Jobs) markRunning(r *Run) {
	j.mu.Lock()
	r.Status = RunRunning
	if r.StartedAt.IsZero() || r.StartedAt.Equal(r.QueuedAt) {
		r.StartedAt = time.Now()
	}
	j.mu.Unlock()
	j.persist(r)
}

func (j *Jobs) claim(runID, agentID string) (*Run, error) {
	if stored, err := j.store.GetRun(context.Background(), runID); err == nil && stored != nil {
		j.mu.Lock()
		j.runs[runID] = stored
		j.ensureLogsLocked(runID, false)
		j.mu.Unlock()
	}
	j.mu.Lock()
	run, ok := j.runs[runID]
	if !ok {
		j.mu.Unlock()
		return nil, fmt.Errorf("run not found")
	}
	if run.AgentID != agentID {
		j.mu.Unlock()
		return nil, fmt.Errorf("run assigned to agent %q", run.AgentID)
	}
	if run.Status != RunAssigned {
		j.mu.Unlock()
		return nil, fmt.Errorf("run status %q cannot be claimed", run.Status)
	}
	run.Status = RunRunning
	if run.StartedAt.IsZero() || run.StartedAt.Equal(run.QueuedAt) {
		run.StartedAt = time.Now()
	}
	out := runRecord(*run)
	j.mu.Unlock()
	j.persist(run)
	return &out, nil
}

func (j *Jobs) runnableForAgent(agentID string) []*Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*Run, 0)
	for _, r := range j.runs {
		if r.AgentID == agentID && r.Status == RunAssigned {
			out = append(out, r)
		}
	}
	return out
}

func latestRunsByID(runs []Run) []Run {
	latest := map[string]Run{}
	for _, run := range runs {
		prev, ok := latest[run.ID]
		if !ok || runStatusRank(run.Status) >= runStatusRank(prev.Status) || runRecordTime(run).After(runRecordTime(prev)) {
			latest[run.ID] = run
		}
	}
	out := make([]Run, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	return out
}

func runStatusRank(status RunStatus) int {
	switch status {
	case RunQueued:
		return 1
	case RunAssigned:
		return 2
	case RunRunning:
		return 3
	case RunDone, RunFailed, RunCancelled:
		return 4
	default:
		return 0
	}
}

func runRecordTime(run Run) time.Time {
	switch {
	case !run.EndedAt.IsZero():
		return run.EndedAt
	case !run.StartedAt.IsZero():
		return run.StartedAt
	case !run.AssignedAt.IsZero():
		return run.AssignedAt
	default:
		return run.QueuedAt
	}
}

func (j *Jobs) hasRunning(topology string) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	for _, r := range j.runs {
		if r.Topology == topology && (r.Status == RunQueued || r.Status == RunAssigned || r.Status == RunRunning) {
			return true
		}
	}
	return false
}

func (j *Jobs) startChild(parent *Run) *Run {
	return j.startWithOptions(parent.Topology, parent.Op, runStartOptions{
		ParentID: parent.ID,
		Revision: parent.Revision,
		PlanID:   parent.PlanID,
		AgentID:  parent.AgentID,
	})
}

func (j *Jobs) finish(r *Run, err error) {
	j.mu.Lock()
	r.EndedAt = time.Now()
	if err != nil {
		r.Status = RunFailed
		r.Err = err.Error()
		r.Recoverable = true
	} else {
		r.Status = RunDone
		r.Err = ""
		r.Recoverable = false
	}
	j.mu.Unlock()
	j.closeLogs(r.ID)
	j.persist(r)
}

func (j *Jobs) replace(r *Run) {
	if r == nil {
		return
	}
	normalizeRunProductFields(r)
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	if r.Status == RunDone || r.Status == RunFailed || r.Status == RunCancelled {
		j.closeLogs(r.ID)
	}
	j.persist(r)
}

func (j *Jobs) get(id string) (*Run, bool) {
	if run, err := j.store.GetRun(context.Background(), id); err == nil && run != nil {
		j.mu.Lock()
		if existing, ok := j.runs[id]; ok {
			if preferRun(existing, run) {
				j.mu.Unlock()
				return existing, true
			}
		} else {
			j.ensureLogsLocked(run.ID, run.Status != RunQueued && run.Status != RunAssigned && run.Status != RunRunning)
		}
		j.runs[id] = run
		j.mu.Unlock()
		return run, true
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	r, ok := j.runs[id]
	return r, ok
}

func preferRun(a, b *Run) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	ar, br := runStatusRank(a.Status), runStatusRank(b.Status)
	if ar != br {
		return ar > br
	}
	return runRecordTime(*a).After(runRecordTime(*b))
}

func (j *Jobs) list(topology string) []*Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*Run, 0, len(j.runs))
	for _, r := range j.runs {
		if topology == "" || r.Topology == topology {
			out = append(out, r)
		}
	}
	return out
}

func (j *Jobs) logWriter(runID string) *Broadcaster {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ensureLogsLocked(runID, false)
}

func (j *Jobs) ensureLogs(runID string, closed bool) *Broadcaster {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ensureLogsLocked(runID, closed)
}

func (j *Jobs) ensureLogsLocked(runID string, closed bool) *Broadcaster {
	if j.logs == nil {
		j.logs = make(map[string]*Broadcaster)
	}
	b, ok := j.logs[runID]
	if !ok {
		b = &Broadcaster{}
		j.logs[runID] = b
	}
	if closed {
		b.Close()
	}
	return b
}

func (j *Jobs) closeLogs(runID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ensureLogsLocked(runID, false).Close()
}
