package runtime

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func requireRecoveryQuiescence(record ActionRecord) error {
	if record.ProviderOperationID == "" || record.Quiescence == "" || record.Quiescence == "returned" || record.Quiescence == "proven" {
		return nil
	}
	if record.Quiescence == "unknown" {
		return fmt.Errorf("%w: provider operation %s is still in-flight without quiescence proof", ErrActionUncertain, record.ProviderOperationID)
	}
	return fmt.Errorf("%w: provider operation %s has invalid quiescence state %q", ErrActionUncertain, record.ProviderOperationID, record.Quiescence)
}

// RecoverActions observes already authorized incomplete actions and commits
// proven outcomes. It never calls Create/Delete or replays a guest command.
// Reset phases that still require an effect must resume via Reset.
func (e *Executor) RecoverActions(ctx context.Context, plan *Plan) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan == nil {
		return fmt.Errorf("recovery requires the pinned plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if e.actions == nil {
		return fmt.Errorf("%w: recovery requires action repository", ErrConvergenceUnsupported)
	}
	stored, err := e.actions.LoadState(ctx, e.topology)
	if err != nil {
		return err
	}
	if stored.Meta.Serial != e.state.Meta.Serial {
		return fmt.Errorf("%w: recovery state serial changed", ErrPlanStale)
	}
	if e.state.TopologyUID != "" && e.state.TopologyUID != stored.TopologyUID {
		return fmt.Errorf("%w: recovery owner changed", ErrObservationUnknown)
	}
	e.state.TopologyUID = stored.TopologyUID
	if err := e.recorder.Begin("recover", plan); err != nil {
		return err
	}
	defer func() { resultErr = e.finishJournal(resultErr) }()
	e.recoveredCreates = map[string]bool{}
	e.recoveredDeletes = map[string]bool{}
	return e.recoverActions(ctx, plan)
}

// recoverActions verifies incomplete actions using the normal observer. It
// never reissues an external effect or an arbitrary guest command.
func (e *Executor) recoverActions(ctx context.Context, plan *Plan) error {
	ctx = secret.WithResolver(ctx, e.resolver())
	if e.actions == nil {
		return nil
	}
	actions, err := e.actions.LoadActions(ctx, e.topology)
	if err != nil {
		return err
	}
	allowed := map[string]controlplane.PlannedChange{}
	for _, change := range plan.Actions {
		if plan.excluded[change.Address.String()] {
			continue
		}
		allowed[change.Address.String()] = change
	}
	for _, record := range actions {
		if record.Status == ActionSucceeded {
			continue
		}
		if err := requireRecoveryQuiescence(record); err != nil {
			if record.Quiescence != "unknown" || record.ProviderOperationID == "" {
				return err
			}
			if err := e.proveActionQuiescence(ctx, &record); err != nil {
				return err
			}
		}
		if record.Action == controlplane.PlanActionReset && !record.EffectComplete {
			return fmt.Errorf("%w: reset %s must resume through reset phase observation", ErrActionUncertain, record.ID)
		}
		if current := e.state.FindResource(record.Resource); current != nil {
			generationMatches := current.Generation == record.Generation
			if record.Action == controlplane.PlanActionReset && record.EffectComplete && current.Generation == record.Generation-1 {
				generationMatches = true
			}
			if !generationMatches || current.OwnerUID != record.OwnerUID || current.ResourceUID != record.ResourceUID {
				return fmt.Errorf("%w: pending action %s does not own current resource generation", ErrObservationUnknown, record.ID)
			}
			if record.Action != controlplane.PlanActionReset && record.Candidate != nil && current.ExternalID != record.Candidate.ExternalID {
				return fmt.Errorf("%w: pending action external identity changed", ErrObservationUnknown)
			}
		}
		change, ok := allowed[record.Resource.String()]
		if !ok {
			return fmt.Errorf("%w: pending action %s lies outside approved scope", ErrPlanStale, record.ID)
		}
		if node := e.graph.Get(record.Resource); node != nil && record.DesiredDigest != "" {
			digest, err := desiredActionDigest(node, change)
			if err != nil {
				return err
			}
			if digest != record.DesiredDigest {
				return fmt.Errorf("%w: pending action target changed", ErrPlanStale)
			}
		}
		if record.Action != controlplane.PlanActionDelete && (record.ReplayForbidden || requiresCompositeCompletion(record.Resource.Type, record.Action)) && !record.EffectComplete {
			return fmt.Errorf("%w: action %s guest initialization, provisioners, or image entry may be incomplete; manual resolution required", ErrActionUncertain, record.ID)
		}
		handler, ok := GetResourceHandler(record.Resource.Type)
		if !ok {
			return fmt.Errorf("%w: %s", ErrConvergenceUnsupported, record.Resource.Type)
		}
		var observed ResourceReadResult
		observeCtx := substrate.WithOperationIdentity(ctx, actionIdentity(record))
		if record.Candidate != nil {
			observed, err = checkedRead(observeCtx, handler, *record.Candidate, e.state)
		} else {
			observed, err = e.observeTarget(observeCtx, record.Resource)
		}
		if err != nil {
			return err
		}
		expected := state.ResourcePresent
		if record.Action == controlplane.PlanActionDelete {
			expected = state.ResourceAbsent
			if change.Action != controlplane.PlanActionDelete && change.Action != controlplane.PlanActionReplace && change.Action != controlplane.PlanActionCreate {
				return fmt.Errorf("%w: pending delete no longer approved", ErrPlanStale)
			}
		}
		if observed.Status != expected {
			return fmt.Errorf("%w: action %s observed %s; no automatic replay", ErrActionUncertain, record.ID, observed.Status)
		}
		if record.Candidate == nil {
			if observed.Resource.Address.IsZero() {
				return fmt.Errorf("%w: action %s has no recoverable identity", ErrActionUncertain, record.ID)
			}
			candidate := observed.Resource
			record.Candidate = &candidate
		}
		record.ExpectedSerial = e.state.Meta.Serial
		step := e.recorder.StepStart(record.Resource.String(), record.Action)
		if err := e.journalError(); err != nil {
			return err
		}
		if record.Action == controlplane.PlanActionReset {
			if err := e.reconcileResetSSHWithEffects(observeCtx, &record, false); err != nil {
				return err
			}
		}
		if err := e.commitVerifiedAction(ctx, record, step); err != nil {
			return err
		}
		if record.Action == controlplane.PlanActionCreate {
			e.recoveredCreates[record.Resource.String()] = true
		}
		if record.Action == controlplane.PlanActionDelete {
			e.recoveredDeletes[record.Resource.String()] = true
		}
	}
	return nil
}

func (e *Executor) proveActionQuiescence(ctx context.Context, record *ActionRecord) error {
	if record == nil {
		return fmt.Errorf("%w: missing action for quiescence proof", ErrActionUncertain)
	}
	if record.Candidate == nil {
		return fmt.Errorf("%w: action %s has no provider handle for quiescence proof", ErrActionUncertain, record.ID)
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(record.Candidate.Driver)
	if err != nil {
		return fmt.Errorf("%w: provider %s unavailable for quiescence proof: %v", ErrActionUncertain, record.Candidate.Driver, err)
	}
	quiescer, ok := nodeDriver.(substrate.OperationQuiescer)
	if !ok {
		return fmt.Errorf("%w: provider %s has no operation quiescence SPI", ErrActionUncertain, record.Candidate.Driver)
	}
	stateDriver, ok := nodeDriver.(driver.NodeState)
	if !ok {
		return fmt.Errorf("%w: provider %s cannot recover provider handle", ErrActionUncertain, record.Candidate.Driver)
	}
	providerState, err := record.Candidate.ProviderState()
	if err != nil {
		return fmt.Errorf("%w: load provider handle: %v", ErrActionUncertain, err)
	}
	provider, err := stateDriver.UnmarshalProviderState(providerState)
	if err != nil {
		return fmt.Errorf("%w: decode provider handle: %v", ErrActionUncertain, err)
	}
	handle := substrate.NodeHandle{ID: record.Candidate.ExternalID, Generation: record.Candidate.Generation, Provider: provider}
	identity := actionIdentity(*record)
	proof, err := quiescer.ProveQuiescence(substrate.WithOperationIdentity(ctx, identity), handle, record.ProviderOperationID, identity)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrActionUncertain, err)
	}
	if err := proof.Validate(identity, record.ProviderOperationID); err != nil {
		return fmt.Errorf("%w: %v", ErrActionUncertain, err)
	}
	record.Quiescence = "proven"
	record.QuiescenceProof = &proof
	return nil
}
