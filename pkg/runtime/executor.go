package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered capability drivers and updates state after each action.
type Executor struct {
	providers           *driver.Registry
	graph               *graph.Graph
	state               *state.State
	logger              io.Writer
	recorder            OperationRecorder
	patchSink           StatePatchSink
	actions             ActionRepository
	recoveredCreates    map[string]bool
	recoveredDeletes    map[string]bool
	activeReset         *ActionRecord
	activeCreate        *ActionRecord
	secretResolver      secret.Resolver
	topology            string
	runID               string
	runQueuedAt         time.Time
	runRevision         string
	resumeRunID         string
	workspaceDir        string
	operation           string
	currentResourceStep int
}

func NewExecutor(g *graph.Graph, s *state.State, providers *driver.Registry) *Executor {
	return &Executor{providers: providers, graph: g, state: s, logger: os.Stdout, recorder: NoopRecorder{}, currentResourceStep: -1}
}

// SetLogger redirects all apply/destroy log output (e.g. to capture it for
// an API SSE stream). Defaults to os.Stdout.
func (e *Executor) SetLogger(w io.Writer) { e.logger = w }

func (e *Executor) SetRecorder(r OperationRecorder) {
	if r == nil {
		e.recorder = NoopRecorder{}
		return
	}
	e.recorder = r
}

func (e *Executor) SetStatePatchSink(sink StatePatchSink) {
	e.patchSink = sink
}

func (e *Executor) SetRunContext(topology, runID string) {
	e.topology = topology
	e.runID = runID
}

func (e *Executor) SetResumeRunID(runID string) { e.resumeRunID = runID }

func (e *Executor) SetRunIncarnation(queuedAt time.Time, revision string) {
	e.runQueuedAt = queuedAt
	e.runRevision = revision
}

// SetWorkspaceDir records the directory a topology's HCL tree is materialised
// into (workspacesDir/<topology>/). Relative provisioner "file" sources resolve
// against it; absolute and ~-expanded sources are left untouched.
func (e *Executor) SetWorkspaceDir(dir string) { e.workspaceDir = dir }

// SetWorkspaceFromHCLFile derives the workspace directory from the topology's
// HCL file path (the directory containing field.sysbox.hcl). Centralising the
// filepath.Dir here keeps every caller from deriving it separately.
func (e *Executor) SetWorkspaceFromHCLFile(path string) {
	e.workspaceDir = filepath.Dir(path)
}

func (e *Executor) SetOperation(operation string) {
	e.operation = operation
}

// SetSecretResolver installs the resolver used to materialise secret references
// (secret://env/…, secret://input/…) during this execution. It defaults to the
// environment resolver when unset. This is a per-execution dependency, like the
// logger and recorder, so each run injects its own inputs without any shared
// mutable global.
func (e *Executor) SetSecretResolver(r secret.Resolver) {
	e.secretResolver = r
}

// resolver returns the active secret resolver, defaulting to the environment.
func (e *Executor) resolver() secret.Resolver {
	if e.secretResolver != nil {
		return e.secretResolver
	}
	return secret.EnvironmentResolver{}
}

func (e *Executor) recordSubstep(parent int, phase string, details map[string]any, fn func() error) error {
	if err := e.journalError(); err != nil {
		return err
	}
	step := e.recorder.SubstepStart(parent, phase, details)
	if err := e.journalError(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		e.recorder.StepFailed(step, err)
		return err
	}
	e.recorder.StepDone(step)
	return e.journalError()
}

func (e *Executor) journalError() error {
	if source, ok := e.recorder.(interface{ Err() error }); ok {
		if err := source.Err(); err != nil {
			return fmt.Errorf("operation journal unavailable: %w", err)
		}
	}
	return nil
}

func (e *Executor) finishJournal(operationErr error) error {
	if err := e.journalError(); err != nil {
		operationErr = errors.Join(operationErr, err)
	}
	e.recorder.Finish(operationErr)
	if err := e.journalError(); err != nil {
		return errors.Join(operationErr, err)
	}
	return operationErr
}

func (e *Executor) substepHook(parent int) NICWireHook {
	if parent < 0 {
		return nil
	}
	return func(phase string, details map[string]any, fn func() error) error {
		return e.recordSubstep(parent, phase, details, fn)
	}
}

func (e *Executor) setCurrentResourceStep(step int) func() {
	prev := e.currentResourceStep
	e.currentResourceStep = step
	return func() {
		e.currentResourceStep = prev
	}
}

func (e *Executor) recordStepExternal(ctx context.Context, step int, id address.Address, action model.PlanActionType) error {
	r := e.state.FindResource(id)
	if r == nil {
		return nil
	}
	if record := e.activeCreate; record != nil && record.Resource.Equal(id) {
		candidate := cloneResetStateResource(*r)
		candidate.OwnerUID = record.OwnerUID
		candidate.ResourceUID = record.ResourceUID
		candidate.Generation = record.Generation
		candidate.ResourceType = id.Type
		if handler, ok := GetResourceHandler(id.Type); ok {
			candidate.SchemaVersion = handler.Schema().Version
			candidate.ExternalID = handler.ExternalID(candidate)
		}
		if node := e.graph.Get(id); node != nil {
			candidate.Dependencies = append([]address.Address(nil), node.Deps...)
		}
		record.Candidate = &candidate
		record.ExpectedSerial = e.state.Meta.Serial
		record.Status = ActionUncertain
		record.EffectComplete = false
		if err := e.actions.CommitActionOutcome(ctx, *record, nil); err != nil {
			return fmt.Errorf("persist create phase identity: %w", err)
		}
		record.Revision++
		e.recorder.StepStateResource(step, resourceLog(candidate))
		return e.journalError()
	}
	if e.activeReset != nil && e.activeReset.Resource.Equal(id) {
		candidate := cloneResetStateResource(*r)
		e.activeReset.Candidate = &candidate
		e.activeReset.ExpectedSerial = e.state.Meta.Serial
		e.activeReset.Status = ActionUncertain
		if err := e.actions.CommitActionOutcome(ctx, *e.activeReset, nil); err != nil {
			return fmt.Errorf("persist reset phase identity: %w", err)
		}
		e.activeReset.Revision++
		e.recorder.StepStateResource(step, resourceLog(candidate))
		return e.journalError()
	}
	externalID := r.Str("id")
	if p, ok := GetResourceHandler(r.Address.Type); ok {
		externalID = p.ExternalID(*r)
	}
	e.recorder.StepExternal(step, r.Driver, externalID, ManagedLabels(e.topology, e.runID, id))
	log := StateResourceLog{
		Type:        r.Address.Type,
		Name:        r.Address.Name,
		Provider:    r.Driver,
		ExternalID:  r.ExternalID,
		Instance:    r.AttributeMap(),
		Attachments: cloneAttachments(r.Attachments),
		Private:     append(json.RawMessage(nil), r.Private...),
		Status:      r.Status,
	}
	e.recorder.StepStateResource(step, log)
	patch := StatePatch{
		Index:    step,
		Resource: id.String(),
		Action:   action,
		Op:       StatePatchUpsert,
		State:    &log,
	}
	e.recorder.StepStatePatch(step, StatePatchUpsert, &log)
	if err := e.journalError(); err != nil {
		return err
	}
	if e.patchSink != nil {
		if err := e.patchSink.ApplyStatePatch(ctx, patch); err != nil {
			return fmt.Errorf("persist state patch for %s: %w", id, err)
		} else {
			e.recorder.StepStateRecorded(step)
		}
	}
	return nil
}

func (e *Executor) recordDeletePatch(ctx context.Context, step int, r state.Resource, action model.PlanActionType) error {
	log := StateResourceLog{
		Type:        r.Address.Type,
		Name:        r.Address.Name,
		Provider:    r.Driver,
		ExternalID:  r.ExternalID,
		Instance:    r.AttributeMap(),
		Attachments: cloneAttachments(r.Attachments),
		Private:     append(json.RawMessage(nil), r.Private...),
		Status:      r.Status,
	}
	e.recorder.StepStatePatch(step, StatePatchDelete, &log)
	if err := e.journalError(); err != nil {
		return err
	}
	if e.patchSink != nil {
		patch := StatePatch{
			Index:    step,
			Resource: r.Address.String(),
			Action:   action,
			Op:       StatePatchDelete,
			State:    &log,
		}
		if err := e.patchSink.ApplyStatePatch(ctx, patch); err != nil {
			return fmt.Errorf("persist delete patch for %s: %w", r.Address, err)
		} else {
			e.recorder.StepStateRecorded(step)
		}
	}
	return e.journalError()
}

func (e *Executor) logf(format string, args ...any) {
	fmt.Fprintf(e.logger, format, args...)
}

// CreateResource dispatches a node in the graph to the right provider
// and records the result in state.
func (e *Executor) CreateResource(ctx context.Context, id address.Address) error {
	node := e.graph.Get(id)
	if node == nil {
		return fmt.Errorf("node %s not in graph", id)
	}

	if p, ok := GetResourceHandler(id.Type); ok {
		res, err := p.Create(ctx, &ProviderContext{exec: e}, node)
		if err != nil {
			if !res.Address.IsZero() {
				e.state.RemoveResource(res.Address)
				e.state.AddResource(res)
			}
			return err
		}
		res.ResourceType = id.Type
		res.SchemaVersion = p.Schema().Version
		res.Dependencies = append([]address.Address(nil), node.Deps...)
		res.Status = state.ResourcePresent
		if res.ExternalID == "" {
			res.ExternalID = p.ExternalID(res)
		}
		e.state.RemoveResource(res.Address)
		e.state.AddResource(res)
		return nil
	}

	return fmt.Errorf("unsupported resource type %q", id.Type)
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	if p, ok := GetResourceHandler(r.Address.Type); ok {
		return p.Delete(ctx, &ProviderContext{exec: e}, r)
	}

	return fmt.Errorf("unsupported resource type %q", r.Address.Type)
}

// -- reference resolution helpers --
//
// After HCL EvalContext lands, references decode to bare strings:
//
//	substrate.docker.light    -> "docker"
//	sysbox_image.alpine.id    -> "alpine"
//
// Quoted "type.name.attr" strings are normalized here as reference literals.

func resolveSubstrateRef(ref string) (string, error) {
	return config.ResolveSubstrateRef(ref)
}

// stateAdapter wraps *state.State to implement substrate.StateReader.
type stateAdapter struct{ st *state.State }

func (a stateAdapter) ResourceInstance(addr address.Address) map[string]any {
	r := a.st.FindResource(addr)
	if r == nil {
		return nil
	}
	return r.AttributeMap()
}

var _ substrate.StateReader = stateAdapter{}

// expandTilde replaces a leading ~ with the current user's home directory.
func expandTilde(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
