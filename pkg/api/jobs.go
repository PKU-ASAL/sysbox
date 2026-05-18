package api

import (
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
type Run struct {
	ID        string    `json:"id"`
	Suite     string    `json:"suite"`
	Op        string    `json:"op"` // "apply" | "destroy"
	Status    RunStatus `json:"status"`
	Err       string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	logs *Broadcaster
}

func (r *Run) LogWriter() *Broadcaster { return r.logs }

// Jobs is an in-memory store for async runs.
type Jobs struct {
	mu   sync.RWMutex
	runs map[string]*Run
}

func newJobs() *Jobs { return &Jobs{runs: make(map[string]*Run)} }

func (j *Jobs) start(suite, op string) *Run {
	r := &Run{
		ID:        uuid.New().String(),
		Suite:     suite,
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
	r.EndedAt = time.Now()
	if err != nil {
		r.Status = RunFailed
		r.Err = err.Error()
	} else {
		r.Status = RunDone
	}
	r.logs.Close()
}

func (j *Jobs) get(id string) (*Run, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	r, ok := j.runs[id]
	return r, ok
}
