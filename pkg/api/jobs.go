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

// Jobs is a run store backed by in-memory map + the API persistence store.
// Per-topology mutexes prevent concurrent apply/destroy on the same topology.
type Jobs struct {
	mu                 sync.RWMutex
	runs               map[string]*controlplane.Run
	runsDir            string // root directory for runs, e.g. "runs"
	store              apiStore
	recoverInterrupted bool
	runLeaseTTL        time.Duration
	expiredPolicy      string
	logs               *RunLogHub
	topologyLocks      *TopologyLocks
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
	return newJobsWithPolicy(runsDir, store, recoverInterrupted, 30*time.Minute, "fail_recoverable")
}

func newJobsWithPolicy(runsDir string, store apiStore, recoverInterrupted bool, runLeaseTTL time.Duration, expiredPolicy string) *Jobs {
	if store == nil {
		store = &localAPIStore{runsDir: runsDir}
	}
	if runLeaseTTL <= 0 {
		runLeaseTTL = 30 * time.Minute
	}
	if expiredPolicy == "" {
		expiredPolicy = "fail_recoverable"
	}
	j := &Jobs{
		runs:               make(map[string]*controlplane.Run),
		runsDir:            runsDir,
		store:              store,
		recoverInterrupted: recoverInterrupted,
		runLeaseTTL:        runLeaseTTL,
		expiredPolicy:      expiredPolicy,
		logs:               newRunLogHub(),
		topologyLocks:      newTopologyLocks(),
	}
	j.load()
	return j
}

// lockTopology acquires a per-topology mutex. Returns a function to unlock.
// This ensures that concurrent apply/destroy requests for the same topology
// are serialised, preventing state file corruption and double-create bugs.
func (j *Jobs) lockTopology(topology string) func() {
	return j.topologyLocks.Lock(topology)
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
		j.logs.Ensure(run.ID, true)
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
		status := controlplane.RunFailed
		errMsg := "server restarted before run completion"
		if cp.Status == "done" {
			status = controlplane.RunDone
			errMsg = ""
		}
		r := &controlplane.Run{
			ID:          cp.RunID,
			ProjectID:   "default",
			Workspace:   cp.Topology,
			Topology:    cp.Topology,
			Op:          cp.Operation,
			Status:      status,
			Err:         errMsg,
			Recoverable: status == controlplane.RunFailed,
			StartedAt:   cp.StartedAt,
			EndedAt:     cp.EndedAt,
		}
		j.logs.Ensure(r.ID, true)
		j.runs[r.ID] = r
	}
}

// persist writes a run record through the configured API store.
func (j *Jobs) persist(r *controlplane.Run) {
	if err := j.store.SaveRun(context.Background(), runRecord(*r)); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: %v\n", err)
	}
}

func runRecord(r controlplane.Run) controlplane.Run {
	normalizeRunProductFields(&r)
	return r
}

func normalizeRunProductFields(r *controlplane.Run) {
	if r == nil {
		return
	}
	if r.Operation == "" {
		r.Operation = r.Op
	}
	if r.Op == "" {
		r.Op = r.Operation
	}
	if r.ProjectID == "" {
		r.ProjectID = "default"
	}
	if r.Workspace == "" {
		r.Workspace = r.Topology
	}
}

func (j *Jobs) start(topology, op string) *controlplane.Run {
	return j.startWithOptions(topology, op, runStartOptions{})
}

func (j *Jobs) startWithOptions(topology, op string, opts runStartOptions) *controlplane.Run {
	now := time.Now()
	r := &controlplane.Run{
		ID:         uuid.New().String(),
		ProjectID:  "default",
		Workspace:  topology,
		Topology:   topology,
		Op:         op,
		Status:     controlplane.RunQueued,
		ParentID:   opts.ParentID,
		Revision:   opts.Revision,
		PlanID:     opts.PlanID,
		AgentID:    opts.AgentID,
		Protocol:   controlplane.AgentProtocolVersion,
		LeaseOwner: "sysbox-api",
		QueuedAt:   now,
		StartedAt:  now,
	}
	normalizeRunProductFields(r)
	r.LeaseOwner = fmt.Sprintf("sysbox-api:%s:%s", r.Op, r.ID)
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	j.logs.Ensure(r.ID, false)
	j.persist(r)
	return r
}

func (j *Jobs) assign(r *controlplane.Run, agentID string) {
	j.mu.Lock()
	r.MarkAssigned(agentID, time.Now())
	j.mu.Unlock()
	j.persist(r)
}

func (j *Jobs) markRunning(r *controlplane.Run) {
	j.mu.Lock()
	r.Status = controlplane.RunRunning
	r.Attempt++
	if r.StartedAt.IsZero() || r.StartedAt.Equal(r.QueuedAt) {
		r.StartedAt = time.Now()
	}
	j.mu.Unlock()
	j.persist(r)
}

func (j *Jobs) claim(runID, agentID string) (*controlplane.Run, error) {
	owner := fmt.Sprintf("%s:%s:%d", agentID, runID, time.Now().UnixNano())
	ttl := j.runLeaseTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if claimed, ok, err := j.store.ClaimRun(context.Background(), runID, agentID, owner, ttl); err != nil {
		return nil, err
	} else if ok && claimed != nil {
		j.mu.Lock()
		j.runs[runID] = claimed
		j.mu.Unlock()
		j.logs.Ensure(runID, false)
		return claimed, nil
	} else if claimed != nil {
		return nil, fmt.Errorf("run status %q cannot be claimed", claimed.Status)
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
	now := time.Now()
	if !run.CanBeClaimedBy(agentID, now) {
		j.mu.Unlock()
		return nil, fmt.Errorf("run status %q cannot be claimed", run.Status)
	}
	run.MarkRunning(owner, ttl, now)
	out := runRecord(*run)
	j.mu.Unlock()
	j.persist(run)
	return &out, nil
}

func (j *Jobs) renewLease(runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, error) {
	renewed, ok, err := j.store.RenewRunLease(context.Background(), runID, agentID, owner, ttl)
	if err != nil {
		return nil, err
	}
	if !ok || renewed == nil {
		return nil, fmt.Errorf("run lease cannot be renewed")
	}
	j.mu.Lock()
	j.runs[runID] = renewed
	j.mu.Unlock()
	return renewed, nil
}

func runLeasable(run controlplane.Run, agentID string, now time.Time) bool {
	return run.CanBeClaimedBy(agentID, now)
}

func (j *Jobs) markExpiredLeases(now time.Time) {
	if j.expiredPolicy != "" && j.expiredPolicy != "fail_recoverable" {
		return
	}
	runs, err := j.store.LoadRuns(context.Background())
	if err != nil {
		return
	}
	for _, run := range latestRunsByID(runs) {
		if run.Status != controlplane.RunRunning || run.LeaseUntil.IsZero() || run.LeaseUntil.After(now) {
			continue
		}
		run.MarkLeaseExpired(now)
		j.replace(&run)
	}
}

func (j *Jobs) runnableForAgent(agentID string) []*controlplane.Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*controlplane.Run, 0)
	for _, r := range j.runs {
		if r.AgentID == agentID && r.Status == controlplane.RunAssigned {
			out = append(out, r)
		}
	}
	return out
}

func latestRunsByID(runs []controlplane.Run) []controlplane.Run {
	latest := map[string]controlplane.Run{}
	for _, run := range runs {
		prev, ok := latest[run.ID]
		if !ok || runStatusRank(run.Status) >= runStatusRank(prev.Status) || runRecordTime(run).After(runRecordTime(prev)) {
			latest[run.ID] = run
		}
	}
	out := make([]controlplane.Run, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	return out
}

func runStatusRank(status controlplane.RunStatus) int {
	return status.Rank()
}

func runRecordTime(run controlplane.Run) time.Time {
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
		if r.Topology == topology && r.Status.IsActive() {
			return true
		}
	}
	return false
}

func (j *Jobs) startChild(parent *controlplane.Run) *controlplane.Run {
	return j.startWithOptions(parent.Topology, parent.Op, runStartOptions{
		ParentID: parent.ID,
		Revision: parent.Revision,
		PlanID:   parent.PlanID,
		AgentID:  parent.AgentID,
	})
}

func (j *Jobs) finish(r *controlplane.Run, err error) {
	j.mu.Lock()
	r.MarkFinished(err, time.Now())
	j.mu.Unlock()
	j.logs.Close(r.ID)
	j.persist(r)
}

func (j *Jobs) replace(r *controlplane.Run) {
	if r == nil {
		return
	}
	normalizeRunProductFields(r)
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	if r.Status.IsTerminal() {
		j.logs.Close(r.ID)
	}
	j.persist(r)
}

func (j *Jobs) get(id string) (*controlplane.Run, bool) {
	if run, err := j.store.GetRun(context.Background(), id); err == nil && run != nil {
		j.mu.Lock()
		if existing, ok := j.runs[id]; ok {
			if preferRun(existing, run) {
				j.mu.Unlock()
				return existing, true
			}
		} else {
			j.logs.Ensure(run.ID, run.Status.IsTerminal())
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

func preferRun(a, b *controlplane.Run) bool {
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

func (j *Jobs) list(topology string) []*controlplane.Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*controlplane.Run, 0, len(j.runs))
	for _, r := range j.runs {
		if topology == "" || r.Topology == topology {
			out = append(out, r)
		}
	}
	return out
}

func (j *Jobs) logWriter(runID string) *Broadcaster {
	return j.logs.Writer(runID)
}
