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
)

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunAssigned  RunStatus = "assigned"
	RunRunning   RunStatus = "running"
	RunDone      RunStatus = "done"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"

	DefaultWorkerID = "local"
)

// Run represents one async apply or destroy operation.
// Fields are JSON-serialisable so they can be persisted by the API store.
type Run struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	Topology    string    `json:"topology"`
	Op          string    `json:"op"` // "apply" | "destroy"
	Status      RunStatus `json:"status"`
	Err         string    `json:"error,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Revision    string    `json:"revision,omitempty"`
	PlanID      string    `json:"plan_id,omitempty"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Recoverable bool      `json:"recoverable,omitempty"`
	LeaseOwner  string    `json:"lease_owner,omitempty"`
	QueuedAt    time.Time `json:"queued_at,omitempty"`
	AssignedAt  time.Time `json:"assigned_at,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`

	logs *Broadcaster // in-memory only; not persisted
}

func (r *Run) LogWriter() *Broadcaster { return r.logs }

// Jobs is a run store backed by in-memory map + the API persistence store.
// Per-topology mutexes prevent concurrent apply/destroy on the same topology.
type Jobs struct {
	mu      sync.RWMutex
	runs    map[string]*Run
	runsDir string // root directory for runs, e.g. "runs"
	store   apiStore

	topologyMu    sync.Mutex
	topologyLocks map[string]*sync.Mutex
}

type runStartOptions struct {
	ParentID string
	Revision string
	PlanID   string
	WorkerID string
}

func newJobs(runsDir string, store apiStore) *Jobs {
	if store == nil {
		store = &localAPIStore{runsDir: runsDir}
	}
	j := &Jobs{runs: make(map[string]*Run), runsDir: runsDir, store: store, topologyLocks: make(map[string]*sync.Mutex)}
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
	for _, r := range markInterruptedRuns(runs) {
		run := r
		normalizeRunProductFields(&run)
		run.logs = &Broadcaster{}
		run.logs.Close()
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
			logs:        &Broadcaster{},
		}
		r.logs.Close()
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
	r.logs = nil
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
	if r.WorkerID == "" {
		r.WorkerID = DefaultWorkerID
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
		WorkerID:   opts.WorkerID,
		LeaseOwner: "sysbox-api",
		QueuedAt:   now,
		StartedAt:  now,
		logs:       &Broadcaster{},
	}
	normalizeRunProductFields(r)
	r.LeaseOwner = fmt.Sprintf("sysbox-api:%s:%s", r.Op, r.ID)
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	j.persist(r)
	return r
}

func (j *Jobs) assign(r *Run, workerID string) {
	j.mu.Lock()
	r.WorkerID = workerID
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
		WorkerID: parent.WorkerID,
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
	r.logs.Close()
	j.persist(r)
}

func (j *Jobs) get(id string) (*Run, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	r, ok := j.runs[id]
	return r, ok
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
