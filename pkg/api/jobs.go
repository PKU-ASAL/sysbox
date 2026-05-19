package api

import (
	"bufio"
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
	RunRunning RunStatus = "running"
	RunDone    RunStatus = "done"
	RunFailed  RunStatus = "failed"
)

// Run represents one async apply or destroy operation.
// Fields are JSON-serialisable so they can be persisted to runs.jsonl.
type Run struct {
	ID        string    `json:"id"`
	Topology  string    `json:"topology"`
	Op        string    `json:"op"` // "apply" | "destroy"
	Status    RunStatus `json:"status"`
	Err       string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	logs *Broadcaster // in-memory only; not persisted
}

func (r *Run) LogWriter() *Broadcaster { return r.logs }

// Jobs is a run store backed by in-memory map + per-topology JSONL files.
// On startup it loads existing runs from disk; on finish it appends a record.
// Per-topology mutexes prevent concurrent apply/destroy on the same topology.
type Jobs struct {
	mu      sync.RWMutex
	runs    map[string]*Run
	runsDir string // root directory for runs, e.g. "runs"

	topologyMu    sync.Mutex
	topologyLocks map[string]*sync.Mutex
}

func newJobs(runsDir string) *Jobs {
	j := &Jobs{runs: make(map[string]*Run), runsDir: runsDir, topologyLocks: make(map[string]*sync.Mutex)}
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

// load scans runs/*/runs.jsonl and populates the in-memory store with
// completed runs from previous server sessions.
func (j *Jobs) load() {
	pattern := filepath.Join(j.runsDir, "*", "runs.jsonl")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var r Run
			if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
				r.logs = &Broadcaster{} // closed; SSE log is gone
				r.logs.Close()
				j.runs[r.ID] = &r
			}
		}
		fh.Close()
	}
}

// persist appends a completed run record to runs/{topology}/runs.jsonl.
func (j *Jobs) persist(r *Run) {
	dir := filepath.Join(j.runsDir, r.Topology)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: mkdir %s: %v\n", dir, err)
		return
	}
	path := filepath.Join(dir, "runs.jsonl")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: open %s: %v\n", path, err)
		return
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	if err := enc.Encode(r); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: encode: %v\n", err)
	}
}

func (j *Jobs) start(topology, op string) *Run {
	r := &Run{
		ID:        uuid.New().String(),
		Topology:  topology,
		Op:        op,
		Status:    RunRunning,
		StartedAt: time.Now(),
		logs:      &Broadcaster{},
	}
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	return r
}

func (j *Jobs) finish(r *Run, err error) {
	j.mu.Lock()
	r.EndedAt = time.Now()
	if err != nil {
		r.Status = RunFailed
		r.Err = err.Error()
	} else {
		r.Status = RunDone
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
