package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

type OperationStatus string

const (
	OperationStarted OperationStatus = "started"
	OperationDone    OperationStatus = "done"
	OperationFailed  OperationStatus = "failed"
)

type OperationStep struct {
	Index         int                         `json:"index"`
	Resource      string                      `json:"resource"`
	Action        controlplane.PlanActionType `json:"action"`
	Kind          string                      `json:"kind,omitempty"`
	Phase         string                      `json:"phase,omitempty"`
	Provider      string                      `json:"provider,omitempty"`
	ExternalID    string                      `json:"external_id,omitempty"`
	Labels        map[string]string           `json:"labels,omitempty"`
	StateResource *StateResourceLog           `json:"state_resource,omitempty"`
	Details       map[string]any              `json:"details,omitempty"`
	Parent        int                         `json:"parent,omitempty"`
	StateRecorded bool                        `json:"state_recorded,omitempty"`
	Status        OperationStatus             `json:"status"`
	StartedAt     time.Time                   `json:"started_at"`
	EndedAt       time.Time                   `json:"ended_at,omitempty"`
	Error         string                      `json:"error,omitempty"`
}

type StatePatchOp string

const (
	StatePatchUpsert StatePatchOp = "upsert"
	StatePatchDelete StatePatchOp = "delete"
)

type StatePatch struct {
	Index    int                         `json:"index"`
	Resource string                      `json:"resource"`
	Action   controlplane.PlanActionType `json:"action"`
	Op       StatePatchOp                `json:"op"`
	State    *StateResourceLog           `json:"state,omitempty"`
	At       time.Time                   `json:"at"`
	Recorded bool                        `json:"recorded,omitempty"`
}

type OperationCheckpoint struct {
	RunID             string                       `json:"run_id"`
	Topology          string                       `json:"topology,omitempty"`
	Operation         string                       `json:"operation"`
	Status            OperationStatus              `json:"status"`
	StartedAt         time.Time                    `json:"started_at"`
	EndedAt           time.Time                    `json:"ended_at,omitempty"`
	LeaseOwner        string                       `json:"lease_owner,omitempty"`
	StateSerialBefore int64                        `json:"state_serial_before,omitempty"`
	StateSerialAfter  int64                        `json:"state_serial_after,omitempty"`
	Plan              []controlplane.PlannedChange `json:"plan,omitempty"`
	PlanSHA256        string                       `json:"plan_sha256,omitempty"`
	Steps             []OperationStep              `json:"steps"`
	StatePatches      []StatePatch                 `json:"state_patches,omitempty"`
}

type StateResourceLog struct {
	Type        string               `json:"type"`
	Name        string               `json:"name"`
	Provider    string               `json:"provider"`
	ExternalID  string               `json:"external_id,omitempty"`
	Instance    map[string]any       `json:"instance"`
	Attachments []state.Attachment   `json:"attachments,omitempty"`
	Private     json.RawMessage      `json:"private,omitempty"`
	Status      state.ResourceStatus `json:"status,omitempty"`
}

type OperationRecorder interface {
	Begin(operation string, plan *Plan) error
	StepStart(resource string, action controlplane.PlanActionType) int
	StepDone(index int)
	StepFailed(index int, err error)
	Finish(err error)
	SetLeaseOwner(owner string)
	SetStateSerialBefore(serial int64)
	SetStateSerialAfter(serial int64)
	StepStartKind(kind, resource string, action controlplane.PlanActionType) int
	StepExternal(index int, provider, externalID string, labels map[string]string)
	StepStateResource(index int, resource StateResourceLog)
	StepStatePatch(index int, op StatePatchOp, resource *StateResourceLog)
	StepStateRecorded(index int)
	MarkResourceStateRecorded()
	SubstepStart(parent int, phase string, details map[string]any) int
}

type StatePatchSink interface {
	ApplyStatePatch(ctx context.Context, patch StatePatch) error
}

type NoopRecorder struct{}

func (NoopRecorder) Begin(string, *Plan) error                         { return nil }
func (NoopRecorder) StepStart(string, controlplane.PlanActionType) int { return -1 }
func (NoopRecorder) StepDone(int)                                      {}
func (NoopRecorder) StepFailed(int, error)                             {}
func (NoopRecorder) Finish(error)                                      {}
func (NoopRecorder) SetLeaseOwner(string)                              {}
func (NoopRecorder) SetStateSerialBefore(int64)                        {}
func (NoopRecorder) SetStateSerialAfter(int64)                         {}
func (NoopRecorder) StepStartKind(string, string, controlplane.PlanActionType) int {
	return -1
}
func (NoopRecorder) StepExternal(int, string, string, map[string]string) {}
func (NoopRecorder) StepStateResource(int, StateResourceLog)             {}
func (NoopRecorder) StepStatePatch(int, StatePatchOp, *StateResourceLog) {}
func (NoopRecorder) StepStateRecorded(int)                               {}
func (NoopRecorder) MarkResourceStateRecorded()                          {}
func (NoopRecorder) SubstepStart(int, string, map[string]any) int        { return -1 }

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
		r.checkpoint.Plan = append([]controlplane.PlannedChange(nil), plan.Actions...)
		fingerprint, err := planActionsSHA256(r.checkpoint.Plan)
		if err != nil {
			return err
		}
		r.checkpoint.PlanSHA256 = fingerprint
	}
	return r.flushLocked()
}

func planActionsSHA256(actions []controlplane.PlannedChange) (string, error) {
	encoded, err := json.Marshal(actions)
	if err != nil {
		return "", fmt.Errorf("fingerprint operation plan: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

func (r *FileRecorder) StepStart(resource string, action controlplane.PlanActionType) int {
	return r.StepStartKind("resource", resource, action)
}

func (r *FileRecorder) StepStartKind(kind, resource string, action controlplane.PlanActionType) int {
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

func (r *FileRecorder) SubstepStart(parent int, phase string, details map[string]any) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := len(r.checkpoint.Steps)
	resource := ""
	action := controlplane.PlanActionNoop
	if parent >= 0 && parent < len(r.checkpoint.Steps) {
		resource = r.checkpoint.Steps[parent].Resource
		action = r.checkpoint.Steps[parent].Action
	}
	r.checkpoint.Steps = append(r.checkpoint.Steps, OperationStep{
		Index:     idx,
		Resource:  resource,
		Action:    action,
		Kind:      "subaction",
		Phase:     phase,
		Parent:    parent,
		Details:   cloneAnyMap(details),
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

func (r *FileRecorder) StepStateResource(index int, resource StateResourceLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	copied := StateResourceLog{
		Type:        resource.Type,
		Name:        resource.Name,
		Provider:    resource.Provider,
		ExternalID:  resource.ExternalID,
		Instance:    cloneAnyMap(resource.Instance),
		Attachments: cloneAttachments(resource.Attachments),
		Private:     append(json.RawMessage(nil), resource.Private...),
		Status:      resource.Status,
	}
	r.checkpoint.Steps[index].StateResource = &copied
	_ = r.flushLocked()
}

func (r *FileRecorder) StepStatePatch(index int, op StatePatchOp, resource *StateResourceLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	step := r.checkpoint.Steps[index]
	patch := StatePatch{
		Index:    index,
		Resource: step.Resource,
		Action:   step.Action,
		Op:       op,
		At:       time.Now().UTC(),
	}
	if resource != nil {
		copied := StateResourceLog{
			Type:        resource.Type,
			Name:        resource.Name,
			Provider:    resource.Provider,
			ExternalID:  resource.ExternalID,
			Instance:    cloneAnyMap(resource.Instance),
			Attachments: cloneAttachments(resource.Attachments),
			Private:     append(json.RawMessage(nil), resource.Private...),
			Status:      resource.Status,
		}
		patch.State = &copied
	}
	r.checkpoint.StatePatches = append(r.checkpoint.StatePatches, patch)
	_ = r.flushLocked()
}

func cloneAttachments(input []state.Attachment) []state.Attachment {
	if len(input) == 0 {
		return nil
	}
	raw, _ := json.Marshal(input)
	var output []state.Attachment
	_ = json.Unmarshal(raw, &output)
	return output
}

func (r *FileRecorder) StepStateRecorded(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.checkpoint.Steps) {
		return
	}
	r.checkpoint.Steps[index].StateRecorded = true
	for i := range r.checkpoint.StatePatches {
		if r.checkpoint.StatePatches[i].Index == index {
			r.checkpoint.StatePatches[i].Recorded = true
		}
	}
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
	for i := range r.checkpoint.StatePatches {
		r.checkpoint.StatePatches[i].Recorded = true
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

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ApplyStatePatch(st *state.State, patch StatePatch) bool {
	switch patch.Op {
	case StatePatchUpsert:
		if patch.State == nil {
			return false
		}
		rec := patch.State
		addr := address.Resource(rec.Type, rec.Name)
		if existing := st.FindResource(addr); existing != nil {
			st.RemoveResource(addr)
		}
		AdoptStateResource(st, *rec, "")
		return true
	case StatePatchDelete:
		if patch.State != nil {
			st.RemoveResource(address.Resource(patch.State.Type, patch.State.Name))
			return true
		}
		addr, err := address.Parse(patch.Resource)
		if err != nil {
			return false
		}
		st.RemoveResource(addr)
		return true
	default:
		return false
	}
}

func UnrecordedStatePatches(cp OperationCheckpoint) []StatePatch {
	out := make([]StatePatch, 0, len(cp.StatePatches))
	for _, patch := range cp.StatePatches {
		if !patch.Recorded {
			out = append(out, patch)
		}
	}
	return out
}

func MarkStatePatchesRecorded(cp *OperationCheckpoint, indexes map[int]bool) {
	if cp == nil || len(indexes) == 0 {
		return
	}
	for i := range cp.StatePatches {
		if indexes[cp.StatePatches[i].Index] {
			cp.StatePatches[i].Recorded = true
		}
	}
	for i := range cp.Steps {
		if indexes[cp.Steps[i].Index] {
			cp.Steps[i].StateRecorded = true
		}
	}
}

func WriteCheckpoint(path string, cp OperationCheckpoint) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func splitResourceAddr(addr string) (string, string, bool) {
	for i := 0; i < len(addr); i++ {
		if addr[i] == '.' {
			if i == 0 || i == len(addr)-1 {
				return "", "", false
			}
			return addr[:i], addr[i+1:], true
		}
	}
	return "", "", false
}

func (r *FileRecorder) flushLocked() error {
	return WriteCheckpoint(r.path, r.checkpoint)
}
