package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	ErrObservationUnknown     = errors.New("observation unknown")
	ErrPlanStale              = errors.New("approved plan no longer matches observed state")
	ErrActionUncertain        = errors.New("action outcome uncertain")
	ErrVerification           = errors.New("action postcondition not verified")
	ErrConvergenceUnsupported = errors.New("observation-driven operation unsupported")
)

func (e *Executor) converge(ctx context.Context, plan *Plan, operation string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan == nil {
		return fmt.Errorf("missing plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := plan.validateBindings(e.graph, e.state); err != nil {
		return err
	}
	if operation != "destroy" {
		if err := e.graph.Validate(); err != nil {
			return err
		}
	}
	if err := e.recorder.Begin(operation, plan); err != nil {
		return err
	}
	defer func() { resultErr = e.finishJournal(resultErr) }()
	if e.actions != nil {
		durable, err := e.actions.LoadState(ctx, e.topology)
		if err != nil {
			return err
		}
		if durable.Meta.Serial != e.state.Meta.Serial {
			return fmt.Errorf("%w: state serial changed", ErrPlanStale)
		}
		if e.state.TopologyUID != "" && e.state.TopologyUID != durable.TopologyUID {
			return fmt.Errorf("%w: topology ownership changed", ErrPlanStale)
		}
		e.state.TopologyUID = durable.TopologyUID
	}
	e.recoveredCreates = make(map[string]bool)
	e.recoveredDeletes = make(map[string]bool)
	if err := e.recoverActions(ctx, plan); err != nil {
		return err
	}
	effective := plan.clone()
	for i := range effective.Actions {
		if e.recoveredCreates[effective.Actions[i].Address.String()] && effective.Actions[i].Action == controlplane.PlanActionReplace {
			effective.Actions[i].Action = controlplane.PlanActionNoop
		}
	}
	plan = effective
	deletions, err := e.orderedDestruction(plan)
	if err != nil {
		return err
	}
	// Validate every target before the first effect. Observe again at the
	// action boundary, after dependencies have finished.
	for _, change := range plan.Actions {
		if operation == "destroy" && change.Action != controlplane.PlanActionDelete && change.Action != controlplane.PlanActionNoop {
			return fmt.Errorf("invalid destroy action %s", change.Action)
		}
		if plan.excluded[change.Address.String()] {
			continue
		}
		if _, err := e.authorizeObserved(ctx, change); err != nil {
			return err
		}
	}
	for _, deletion := range deletions {
		needed, err := e.authorizeObserved(ctx, deletion)
		if err != nil {
			return err
		}
		if needed {
			if err := e.runAction(ctx, deletion); err != nil {
				return err
			}
		}
	}
	for _, change := range plan.Actions {
		if plan.excluded[change.Address.String()] {
			continue
		}
		if change.Action == controlplane.PlanActionDelete {
			continue
		}
		if change.Action == controlplane.PlanActionReplace {
			change.Action = controlplane.PlanActionCreate
		}
		needed, err := e.authorizeObserved(ctx, change)
		if err != nil {
			return err
		}
		if !needed {
			continue
		}
		switch change.Action {
		case controlplane.PlanActionCreate, controlplane.PlanActionRead, controlplane.PlanActionDelete:
			if err := e.runAction(ctx, change); err != nil {
				return err
			}
		}
	}
	return nil
}

// Destruction follows persisted dependencies, even when storage sorts records
// differently. A dependent outside the approved destructive scope invalidates
// the plan instead of silently adding another replacement.
func (e *Executor) orderedDestruction(plan *Plan) ([]controlplane.PlannedChange, error) {
	selected := map[string]controlplane.PlannedChange{}
	for _, change := range plan.Actions {
		if change.Action == controlplane.PlanActionDelete || change.Action == controlplane.PlanActionReplace {
			if e.state.FindResource(change.Address) != nil {
				change.Action = controlplane.PlanActionDelete
				selected[change.Address.String()] = change
			}
		}
	}
	children := map[string][]string{}
	for _, resource := range e.state.Resources {
		for _, dependency := range resource.Dependencies {
			if _, destroying := selected[dependency.String()]; !destroying {
				continue
			}
			if _, destroying := selected[resource.Address.String()]; !destroying {
				return nil, fmt.Errorf("%w: %s still depends on destructive target %s", ErrPlanStale, resource.Address, dependency)
			}
			children[dependency.String()] = append(children[dependency.String()], resource.Address.String())
		}
	}
	visited := map[string]int{}
	out := []controlplane.PlannedChange{}
	var visit func(string) error
	visit = func(key string) error {
		if visited[key] == 2 {
			return nil
		}
		if visited[key] == 1 {
			return fmt.Errorf("%w: persisted dependency cycle", ErrPlanStale)
		}
		visited[key] = 1
		for _, child := range children[key] {
			if err := visit(child); err != nil {
				return err
			}
		}
		visited[key] = 2
		out = append(out, selected[key])
		return nil
	}
	for _, change := range plan.Actions {
		if _, ok := selected[change.Address.String()]; ok {
			if err := visit(change.Address.String()); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (e *Executor) observeTarget(ctx context.Context, id address.Address) (ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ResourceReadResult{}, err
	}
	handler, ok := GetResourceHandler(id.Type)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("%w: handler %s", ErrConvergenceUnsupported, id.Type)
	}
	if current := e.state.FindResource(id); current != nil {
		return checkedRead(ctx, handler, *current, e.state)
	}
	node := e.graph.Get(id)
	if node == nil {
		return ResourceReadResult{}, fmt.Errorf("%w: no persisted identity for %s", ErrObservationUnknown, id)
	}
	discovery, ok := handler.(ResourceDiscoverer)
	if !ok {
		return ResourceReadResult{}, fmt.Errorf("%w: %s has no managed discovery", ErrConvergenceUnsupported, id)
	}
	if _, ok := substrate.OperationIdentityFromContext(ctx); !ok {
		if e.state.TopologyUID == "" {
			return ResourceReadResult{}, fmt.Errorf("%w: missing topology ownership", ErrObservationUnknown)
		}
		identity := substrate.OperationIdentity{ActionID: "observe", TopologyUID: e.state.TopologyUID, OwnerUID: e.state.TopologyUID, ResourceUID: derivedResourceUID(e.state.TopologyUID, id), Generation: 1}
		if e.actions != nil {
			actions, err := e.actions.LoadActions(ctx, e.topology)
			if err != nil {
				return ResourceReadResult{}, err
			}
			for _, a := range actions {
				if a.Resource.Equal(id) && a.Generation >= identity.Generation {
					identity.Generation = a.Generation
					identity.ResourceUID = a.ResourceUID
					if a.Status == ActionSucceeded {
						identity.Generation++
					}
				}
			}
		}
		ctx = substrate.WithOperationIdentity(ctx, identity)
	}
	observed, err := discovery.Discover(ctx, &ProviderContext{exec: e}, node)
	return validateObservation(observed, err, id)
}

func checkedRead(ctx context.Context, handler ResourceHandler, current state.Resource, st *state.State) (ResourceReadResult, error) {
	observed, err := readResourceWithAttachments(ctx, handler, current, st)
	observed, err = validateObservation(observed, err, current.Address)
	if err != nil {
		return observed, err
	}
	if observed.Status != state.ResourceAbsent && !observed.Resource.Address.IsZero() {
		if !observed.Resource.Address.Equal(current.Address) || (current.ExternalID != "" && observed.Resource.ExternalID != "" && current.ExternalID != observed.Resource.ExternalID) {
			return observed, fmt.Errorf("%w: identity conflict for %s", ErrObservationUnknown, current.Address)
		}
	}
	return observed, nil
}

func validateObservation(observed ResourceReadResult, err error, id address.Address) (ResourceReadResult, error) {
	if err != nil {
		return observed, fmt.Errorf("%w: %s: %w", ErrObservationUnknown, id, err)
	}
	if observed.Status == "" || observed.Status == state.ResourceUnknown {
		return observed, fmt.Errorf("%w: %s: %s", ErrObservationUnknown, id, observed.Reason)
	}
	if obs := observed.Observation; obs != nil && obs.ExternalID != "" && observed.Resource.ExternalID != "" && obs.ExternalID != observed.Resource.ExternalID {
		return observed, fmt.Errorf("%w: external identity conflict for %s", ErrObservationUnknown, id)
	}
	return observed, nil
}

func (e *Executor) authorizeObserved(ctx context.Context, change controlplane.PlannedChange) (bool, error) {
	if err := e.journalError(); err != nil {
		return false, err
	}
	if change.Action == controlplane.PlanActionUnknown || change.Action == controlplane.PlanActionReset {
		return false, fmt.Errorf("%w: action %s for %s", ErrConvergenceUnsupported, change.Action, change.Address)
	}
	if change.Action == controlplane.PlanActionDelete && e.state.FindResource(change.Address) == nil {
		if e.recoveredDeletes[change.Address.String()] {
			return false, nil
		}
		observed, err := e.observeTarget(ctx, change.Address)
		if err != nil {
			return false, err
		}
		if observed.Status == state.ResourceAbsent {
			return false, nil
		}
		return false, fmt.Errorf("%w: delete target %s has no persisted managed identity", ErrObservationUnknown, change.Address)
	}
	observed, err := e.observeTarget(ctx, change.Address)
	if err != nil {
		return false, err
	}
	switch change.Action {
	case controlplane.PlanActionNoop:
		if observed.Status != state.ResourcePresent {
			return false, fmt.Errorf("%w: %s is %s; a new plan is required", ErrPlanStale, change.Address, observed.Status)
		}
		return false, nil
	case controlplane.PlanActionDelete:
		if resourcePreventDestroy(e.graph.Get(change.Address), e.state.FindResource(change.Address)) {
			return false, fmt.Errorf("prevent_destroy blocks %s", change.Address)
		}
		return true, nil
	case controlplane.PlanActionReplace:
		if resourcePreventDestroy(e.graph.Get(change.Address), e.state.FindResource(change.Address)) {
			return false, fmt.Errorf("prevent_destroy blocks %s", change.Address)
		}
		return true, nil
	case controlplane.PlanActionRead:
		return true, nil
	case controlplane.PlanActionCreate:
		if observed.Status == state.ResourceAbsent {
			return true, nil
		}
		if current := e.state.FindResource(change.Address); current != nil && observed.Status == state.ResourcePresent {
			h, _ := GetResourceHandler(change.Address.Type)
			diff, err := h.PlanDiff(e.graph.Get(change.Address), current)
			if err != nil {
				return false, err
			}
			if diff.Action == controlplane.PlanActionNoop {
				return false, nil
			}
		}
		return false, fmt.Errorf("%w: create target %s already exists or is drifted", ErrPlanStale, change.Address)
	}
	return false, fmt.Errorf("unsupported action %s", change.Action)
}

func (e *Executor) newAction(ctx context.Context, change controlplane.PlannedChange) (ActionRecord, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ActionRecord{}, err
	}
	if e.state.TopologyUID == "" {
		return ActionRecord{}, fmt.Errorf("%w: missing durable topology ownership", ErrObservationUnknown)
	}
	record := ActionRecord{ID: hex.EncodeToString(id[:]), Topology: e.topology, RunID: e.runID, Resource: change.Address, Action: change.Action, Status: ActionIntentCommitted, OwnerUID: e.state.TopologyUID, ResourceUID: derivedResourceUID(e.state.TopologyUID, change.Address), Generation: 1, StateSerial: e.state.Meta.Serial}
	record.RunQueuedAt, record.RunRevision = e.runQueuedAt, e.runRevision
	record.ExpectedSerial = e.state.Meta.Serial
	if node := e.graph.Get(change.Address); node != nil {
		digest, err := desiredActionDigest(node, change)
		if err != nil {
			return record, err
		}
		record.DesiredDigest = digest
		if cfg, ok := node.Data.(*config.NodeConfig); ok {
			record.ReplayForbidden = change.Action != controlplane.PlanActionDelete && len(cfg.Provisioners) > 0
		}
	}
	if requiresCompositeCompletion(change.Address.Type, change.Action) {
		record.ReplayForbidden = true
	}
	if current := e.state.FindResource(change.Address); current != nil {
		candidate := cloneResetStateResource(*current)
		record.Candidate = &candidate
		record.Generation = current.Generation
		record.ResourceUID = current.ResourceUID
		if current.OwnerUID != record.OwnerUID || current.Generation < 1 || current.ResourceUID == "" {
			return record, fmt.Errorf("%w: unverified resource ownership for %s", ErrObservationUnknown, change.Address)
		}
	}
	actions, err := e.actions.LoadActions(ctx, e.topology)
	if err != nil {
		return record, err
	}
	maxGeneration := int64(0)
	for _, old := range actions {
		if old.Resource.Equal(change.Address) && old.Generation > maxGeneration {
			maxGeneration = old.Generation
			record.ResourceUID = old.ResourceUID
		}
	}
	if maxGeneration > 0 {
		record.Generation = maxGeneration
		if change.Action == controlplane.PlanActionCreate || change.Action == controlplane.PlanActionReset {
			record.Generation++
		}
	}
	if change.Action == controlplane.PlanActionReset && record.Candidate != nil && record.Generation <= record.Candidate.Generation {
		record.Generation = record.Candidate.Generation + 1
	}
	return record, nil
}

func (e *Executor) runAction(ctx context.Context, change controlplane.PlannedChange) error {
	if e.actions == nil {
		return fmt.Errorf("%w: durable action repository required for %s", ErrConvergenceUnsupported, change.Address)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := e.newAction(ctx, change)
	if err != nil {
		return err
	}
	if err := e.actions.CommitActionIntent(ctx, record); err != nil {
		return fmt.Errorf("commit action intent: %w", err)
	}
	record.Revision++
	ctx = substrate.WithOperationIdentity(ctx, actionIdentity(record))
	step := e.recorder.StepStart(change.Address.String(), change.Action)
	if err := e.journalError(); err != nil {
		return err
	}
	staged, err := cloneExecutionState(e.state)
	if err != nil {
		return err
	}
	worker := *e
	worker.state = staged
	worker.patchSink = nil
	worker.currentResourceStep = step
	handler, _ := GetResourceHandler(change.Address.Type)
	var effectErr error
	if change.Action == controlplane.PlanActionDelete {
		current := e.state.FindResource(change.Address)
		if current == nil {
			return fmt.Errorf("%w: identity disappeared", ErrPlanStale)
		}
		before, readErr := checkedRead(ctx, handler, *current, e.state)
		if readErr != nil {
			return e.uncertainAction(ctx, record, step, readErr)
		}
		if before.Status != state.ResourceAbsent {
			effectErr = worker.DestroyResource(ctx, *current)
		}
	} else {
		effectErr = worker.recordSubstep(step, "create_resource", map[string]any{"resource": change.Address.String()}, func() error { return worker.CreateResource(ctx, change.Address) })
		if candidate := worker.state.FindResource(change.Address); candidate != nil {
			copy := cloneResetStateResource(*candidate)
			copy.OwnerUID = record.OwnerUID
			copy.ResourceUID = record.ResourceUID
			copy.Generation = record.Generation
			record.Candidate = &copy
		}
	}
	if effectErr != nil {
		return e.uncertainAction(ctx, record, step, effectErr)
	}
	if record.Candidate == nil {
		return e.uncertainAction(ctx, record, step, fmt.Errorf("provider returned no recoverable identity"))
	}
	record.EffectComplete = true
	record.Status = ActionUncertain
	record.Error = "awaiting postcondition verification"
	if err := e.actions.CommitActionOutcome(ctx, record, nil); err != nil {
		return fmt.Errorf("persist verification identity: %w", err)
	}
	record.Revision++
	if _, referenceOnly := handler.(ReferenceOnlyResource); referenceOnly && change.Action == controlplane.PlanActionDelete {
		if worker.state.FindResource(change.Address) != nil {
			return e.uncertainAction(ctx, record, step, ErrVerification)
		}
		return e.commitVerifiedAction(ctx, record, step)
	}
	observed, err := checkedRead(ctx, handler, *record.Candidate, e.state)
	if err != nil {
		return e.uncertainAction(ctx, record, step, err)
	}
	expected := state.ResourcePresent
	if change.Action == controlplane.PlanActionDelete {
		expected = state.ResourceAbsent
	}
	if observed.Status != expected {
		return e.uncertainAction(ctx, record, step, fmt.Errorf("%w: %s observed %s, expected %s", ErrVerification, change.Address, observed.Status, expected))
	}
	return e.commitVerifiedAction(ctx, record, step)
}

func actionIdentity(record ActionRecord) substrate.OperationIdentity {
	return substrate.OperationIdentity{ActionID: record.ID, TopologyUID: record.OwnerUID, OwnerUID: record.OwnerUID, ResourceUID: record.ResourceUID, Generation: record.Generation}
}

func derivedResourceUID(topologyUID string, id address.Address) string {
	raw := append(append([]byte(topologyUID), 0), []byte(id.String())...)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func desiredActionDigest(node *graph.Node, change controlplane.PlannedChange) (string, error) {
	digest, err := desiredHash(node)
	if err != nil {
		return "", err
	}
	if change.Action == controlplane.PlanActionReset {
		raw, err := json.Marshal(change.Changes)
		if err != nil {
			return "", err
		}
		digest = sha256Hex(append([]byte(digest), raw...))
	}
	return digest, nil
}

func (e *Executor) uncertainAction(ctx context.Context, record ActionRecord, step int, cause error) error {
	record.Status = ActionUncertain
	record.Error = cause.Error()
	err := fmt.Errorf("%w: %s: %w", ErrActionUncertain, record.Resource, cause)
	// Cancellation can prevent this update. The durable intent still prevents
	// blind replay after restart.
	if saveErr := e.actions.CommitActionOutcome(ctx, record, nil); saveErr != nil {
		err = errors.Join(err, fmt.Errorf("persist uncertain action: %w", saveErr))
	}
	e.recorder.StepFailed(step, err)
	return err
}

func resourceLog(r state.Resource) StateResourceLog {
	return StateResourceLog{ResourceType: r.ResourceType, SchemaVersion: r.SchemaVersion, OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, Dependencies: append([]address.Address(nil), r.Dependencies...), Type: r.Address.Type, Name: r.Address.Name, Provider: r.Driver, ExternalID: r.ExternalID, Instance: r.AttributeMap(), Attachments: cloneAttachments(r.Attachments), Private: append(json.RawMessage(nil), r.Private...), Status: r.Status}
}

func (e *Executor) commitVerifiedAction(ctx context.Context, record ActionRecord, step int) error {
	record.ExpectedSerial = e.state.Meta.Serial
	log := resourceLog(*record.Candidate)
	patch := StatePatch{Index: step, Resource: record.Resource.String(), Action: record.Action, Op: StatePatchUpsert, State: &log}
	if record.Action == controlplane.PlanActionDelete {
		patch.Op = StatePatchDelete
	}
	record.Status = ActionSucceeded
	record.Error = ""
	if err := e.actions.CommitActionOutcome(ctx, record, &patch); err != nil {
		e.recorder.StepFailed(step, err)
		return fmt.Errorf("commit verified action %s: %w", record.ID, err)
	}
	if patch.Op == StatePatchDelete {
		e.state.RemoveResource(record.Resource)
	} else {
		e.state.RemoveResource(record.Resource)
		e.state.AddResource(cloneResetStateResource(*record.Candidate))
	}
	committed, err := e.actions.LoadState(ctx, e.topology)
	if err != nil {
		return fmt.Errorf("reload committed state: %w", err)
	}
	e.state.Meta = committed.Meta
	e.recorder.StepExternal(step, record.Candidate.Driver, record.Candidate.ExternalID, ManagedLabels(e.topology, e.runID, record.Resource))
	e.recorder.StepStateResource(step, log)
	e.recorder.StepStatePatch(step, patch.Op, &log)
	e.recorder.StepStateRecorded(step)
	e.recorder.StepDone(step)
	return e.journalError()
}

func cloneExecutionState(st *state.State) (*state.State, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	var out state.State
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.Meta = st.Meta
	return &out, nil
}
