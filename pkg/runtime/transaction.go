package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type OperationStatus string

const (
	OperationStarted OperationStatus = "started"
	OperationDone    OperationStatus = "done"
	OperationFailed  OperationStatus = "failed"
)

type OperationStep struct {
	Index     int             `json:"index"`
	Resource  string          `json:"resource"`
	Action    PlanActionType  `json:"action"`
	Status    OperationStatus `json:"status"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type OperationCheckpoint struct {
	RunID     string          `json:"run_id"`
	Topology  string          `json:"topology,omitempty"`
	Operation string          `json:"operation"`
	Status    OperationStatus `json:"status"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at,omitempty"`
	Steps     []OperationStep `json:"steps"`
}

type OperationRecorder interface {
	Begin(operation string, plan *Plan) error
	StepStart(resource string, action PlanActionType) int
	StepDone(index int)
	StepFailed(index int, err error)
	Finish(err error)
}

type NoopRecorder struct{}

func (NoopRecorder) Begin(string, *Plan) error            { return nil }
func (NoopRecorder) StepStart(string, PlanActionType) int { return -1 }
func (NoopRecorder) StepDone(int)                         {}
func (NoopRecorder) StepFailed(int, error)                {}
func (NoopRecorder) Finish(error)                         {}

type FileRecorder struct {
	mu         sync.Mutex
	path       string
	checkpoint OperationCheckpoint
}

func NewFileRecorder(path, runID, topology string) *FileRecorder {
	return &FileRecorder{
		path: path,
		checkpoint: OperationCheckpoint{
			RunID:     runID,
			Topology:  topology,
			Status:    OperationStarted,
			StartedAt: time.Now().UTC(),
		},
	}
}

func (r *FileRecorder) Begin(operation string, _ *Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.Operation = operation
	r.checkpoint.Status = OperationStarted
	r.checkpoint.StartedAt = time.Now().UTC()
	return r.flushLocked()
}

func (r *FileRecorder) StepStart(resource string, action PlanActionType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := len(r.checkpoint.Steps)
	r.checkpoint.Steps = append(r.checkpoint.Steps, OperationStep{
		Index:     idx,
		Resource:  resource,
		Action:    action,
		Status:    OperationStarted,
		StartedAt: time.Now().UTC(),
	})
	_ = r.flushLocked()
	return idx
}

func (r *FileRecorder) StepDone(index int) {
	r.finishStep(index, nil)
}

func (r *FileRecorder) StepFailed(index int, err error) {
	r.finishStep(index, err)
}

func (r *FileRecorder) Finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.EndedAt = time.Now().UTC()
	if err != nil {
		r.checkpoint.Status = OperationFailed
	} else {
		r.checkpoint.Status = OperationDone
	}
	_ = r.flushLocked()
}

func (r *FileRecorder) finishStep(index int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	step := &r.checkpoint.Steps[index]
	step.EndedAt = time.Now().UTC()
	if err != nil {
		step.Status = OperationFailed
		step.Error = err.Error()
	} else {
		step.Status = OperationDone
	}
	_ = r.flushLocked()
}

func (r *FileRecorder) flushLocked() error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}
	data, err := json.MarshalIndent(&r.checkpoint, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
