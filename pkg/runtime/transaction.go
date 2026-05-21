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
	Index         int               `json:"index"`
	Resource      string            `json:"resource"`
	Action        PlanActionType    `json:"action"`
	Kind          string            `json:"kind,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	ExternalID    string            `json:"external_id,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	StateRecorded bool              `json:"state_recorded,omitempty"`
	Status        OperationStatus   `json:"status"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       time.Time         `json:"ended_at,omitempty"`
	Error         string            `json:"error,omitempty"`
}

type OperationCheckpoint struct {
	RunID             string          `json:"run_id"`
	Topology          string          `json:"topology,omitempty"`
	Operation         string          `json:"operation"`
	Status            OperationStatus `json:"status"`
	StartedAt         time.Time       `json:"started_at"`
	EndedAt           time.Time       `json:"ended_at,omitempty"`
	LeaseOwner        string          `json:"lease_owner,omitempty"`
	StateSerialBefore int64           `json:"state_serial_before,omitempty"`
	StateSerialAfter  int64           `json:"state_serial_after,omitempty"`
	Plan              []PlanAction    `json:"plan,omitempty"`
	Steps             []OperationStep `json:"steps"`
}

type OperationRecorder interface {
	Begin(operation string, plan *Plan) error
	StepStart(resource string, action PlanActionType) int
	StepDone(index int)
	StepFailed(index int, err error)
	Finish(err error)
	SetLeaseOwner(owner string)
	SetStateSerialBefore(serial int64)
	SetStateSerialAfter(serial int64)
	StepStartKind(kind, resource string, action PlanActionType) int
	StepExternal(index int, provider, externalID string, labels map[string]string)
	StepStateRecorded(index int)
	MarkResourceStateRecorded()
}

type NoopRecorder struct{}

func (NoopRecorder) Begin(string, *Plan) error            { return nil }
func (NoopRecorder) StepStart(string, PlanActionType) int { return -1 }
func (NoopRecorder) StepDone(int)                         {}
func (NoopRecorder) StepFailed(int, error)                {}
func (NoopRecorder) Finish(error)                         {}
func (NoopRecorder) SetLeaseOwner(string)                 {}
func (NoopRecorder) SetStateSerialBefore(int64)           {}
func (NoopRecorder) SetStateSerialAfter(int64)            {}
func (NoopRecorder) StepStartKind(string, string, PlanActionType) int {
	return -1
}
func (NoopRecorder) StepExternal(int, string, string, map[string]string) {}
func (NoopRecorder) StepStateRecorded(int)                               {}
func (NoopRecorder) MarkResourceStateRecorded()                          {}

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

func (r *FileRecorder) Begin(operation string, plan *Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.Operation = operation
	r.checkpoint.Status = OperationStarted
	r.checkpoint.StartedAt = time.Now().UTC()
	if plan != nil {
		r.checkpoint.Plan = append([]PlanAction(nil), plan.Actions...)
	}
	return r.flushLocked()
}

func (r *FileRecorder) StepStart(resource string, action PlanActionType) int {
	return r.StepStartKind("resource", resource, action)
}

func (r *FileRecorder) StepStartKind(kind, resource string, action PlanActionType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := len(r.checkpoint.Steps)
	r.checkpoint.Steps = append(r.checkpoint.Steps, OperationStep{
		Index:     idx,
		Resource:  resource,
		Action:    action,
		Kind:      kind,
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

func (r *FileRecorder) SetLeaseOwner(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.LeaseOwner = owner
	_ = r.flushLocked()
}

func (r *FileRecorder) SetStateSerialBefore(serial int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.StateSerialBefore = serial
	_ = r.flushLocked()
}

func (r *FileRecorder) SetStateSerialAfter(serial int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoint.StateSerialAfter = serial
	_ = r.flushLocked()
}

func (r *FileRecorder) StepExternal(index int, provider, externalID string, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	step := &r.checkpoint.Steps[index]
	step.Provider = provider
	step.ExternalID = externalID
	if len(labels) > 0 {
		step.Labels = cloneLabels(labels)
	}
	_ = r.flushLocked()
}

func (r *FileRecorder) StepStateRecorded(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	r.checkpoint.Steps[index].StateRecorded = true
	_ = r.flushLocked()
}

func (r *FileRecorder) MarkResourceStateRecorded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.checkpoint.Steps {
		if r.checkpoint.Steps[i].Kind == "resource" && r.checkpoint.Steps[i].Status == OperationDone {
			r.checkpoint.Steps[i].StateRecorded = true
		}
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

func cloneLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
