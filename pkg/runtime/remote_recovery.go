package runtime

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ObserveRunActions never mutates state or calls a provider effect. A missing
// candidate is unknown, not proof that an external resource is absent.
func ObserveRunActions(ctx context.Context, repo ActionRepository, topology, runID string) (*state.State, []controlplane.RecoveredResource, error) {
	st, err := repo.LoadState(ctx, topology)
	if err != nil {
		return nil, nil, err
	}
	records, err := repo.LoadActions(ctx, topology)
	if err != nil {
		return nil, nil, err
	}
	out := []controlplane.RecoveredResource{}
	for _, record := range records {
		if record.RunID != runID {
			continue
		}
		if record.OwnerUID == "" || record.OwnerUID != st.TopologyUID || record.ResourceUID == "" || record.Generation < 1 {
			return nil, nil, fmt.Errorf("%w: recovery journal identity", ErrObservationUnknown)
		}

		candidate := record.Candidate
		if candidate == nil {
			candidate = st.FindResource(record.Resource)
		}
		item := observeIntentResource(ctx, record, candidate, st)
		if record.Action == controlplane.PlanActionReset {
			item.Status = "unknown"
			if candidate != nil {
				item.ExternalID = candidate.ExternalID
			}
			if record.Candidate != nil && record.Candidate.OwnerUID == record.OwnerUID && record.Candidate.ResourceUID == record.ResourceUID && record.Candidate.Generation > 0 && record.Candidate.Generation <= record.Generation {
				source := record
				source.Generation = record.Candidate.Generation
				// The original phase proves replacement creation was not admitted. Read
				// the old generation using the same residual-absence SPI as deletion;
				// this changes no journal and invokes no provider effect.
				if record.ResetBeforeReplacement() {
					source.Action = controlplane.PlanActionDelete
				}
				observed := observeIntentResource(ctx, source, record.Candidate, st)
				item.ResetSource = &observed
			}
			if record.Replacement != nil && record.Replacement.OwnerUID == record.OwnerUID && record.Replacement.ResourceUID == record.ResourceUID && record.Replacement.Generation == record.Generation {
				observed := observeIntentResource(ctx, record, record.Replacement, st)
				item.ResetReplacement = &observed
			}
		}
		out = append(out, item)
	}
	after, err := repo.LoadState(ctx, topology)
	if err != nil {
		return nil, nil, err
	}
	if after.TopologyUID != st.TopologyUID || after.Meta.Serial != st.Meta.Serial {
		return nil, nil, ErrPlanStale
	}
	afterActions, err := repo.LoadActions(ctx, topology)
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(records, afterActions) {
		return nil, nil, ErrPlanStale
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActionID < out[j].ActionID })
	return st, out, nil
}

// observeIntentResource reads one immutable generation; it does not infer the
// outcome of the composite action containing it.
func observeIntentResource(ctx context.Context, record ActionRecord, candidate *state.Resource, st *state.State) controlplane.RecoveredResource {
	item := controlplane.RecoveredResource{ActionID: record.ID, Resource: record.Resource.String(), OwnerUID: record.OwnerUID, ResourceUID: record.ResourceUID, Generation: record.Generation, Status: "unknown", JournalStatus: string(record.Status)}

	if candidate != nil && candidate.OwnerUID == record.OwnerUID && candidate.ResourceUID == record.ResourceUID && candidate.Generation == record.Generation {
		item.ExternalID = candidate.ExternalID
		if record.Resource.Type == "sysbox_node" {
			if node, err := driver.DefaultRegistry.RequireNode(candidate.Driver); err == nil {
				if local, ok := node.(interface {
					HostFenceScope(context.Context) (string, error)
				}); ok {
					if scope, err := local.HostFenceScope(ctx); err == nil {
						item.ProviderScope = scope
					}
				}
			}
		}
		if handler, ok := GetResourceHandler(record.Resource.Type); ok {
			observed, err := checkedRead(substrate.WithOperationIdentity(ctx, actionIdentity(record)), handler, *candidate, st)
			if err == nil && (observed.Status == state.ResourcePresent || observed.Status == state.ResourceAbsent) {
				if observed.Status == state.ResourceAbsent || observed.Resource.OwnerUID == candidate.OwnerUID && observed.Resource.ResourceUID == candidate.ResourceUID && observed.Resource.Generation == candidate.Generation {
					item.Status = string(observed.Status)
				}
			}
		}
		if item.Status == "absent" && item.ProviderScope == "local-only" && record.Action == controlplane.PlanActionDelete {
			node, err := driver.DefaultRegistry.RequireNode(candidate.Driver)
			codec, codecErr := driver.DefaultRegistry.RequireNodeState(candidate.Driver)
			if err == nil && codecErr == nil {
				if proof, ok := node.(interface {
					HostFenceDeletionAbsent(context.Context, substrate.NodeHandle) error
				}); ok {
					if handle, err := candidate.ReconstructHandle(codec); err == nil && proof.HostFenceDeletionAbsent(ctx, handle) == nil {
						item.DeleteResidueAbsent = true
					}
				}
			}
		}
	}
	return item
}

// RecoverRunActions restricts the existing observation-only recovery engine to
// this journal owner. It cannot create, delete, or replay arbitrary guest work.
func (e *Executor) RecoverRunActions(ctx context.Context, runID string) error {
	if e.actions == nil {
		return ErrConvergenceUnsupported
	}
	all, err := e.actions.LoadActions(ctx, e.topology)
	if err != nil {
		return err
	}
	plan := &Plan{}
	seen := map[string]bool{}
	count := 0
	for _, record := range all {
		if record.RunID == runID && record.DispositionID != "" {
			return ErrActionUncertain
		}
		if record.RunID != runID {
			if !ActionSettled(record) {
				return ErrActionUncertain
			}
			continue
		}
		count++
		if ActionSettled(record) {
			continue
		}
		if seen[record.Resource.String()] {
			return ErrActionUncertain
		}
		seen[record.Resource.String()] = true
		change := controlplane.PlannedChange{Address: record.Resource, Action: record.Action, Reason: "recover authorized owning-agent journal"}
		if record.Action == controlplane.PlanActionReset {
			reset, err := BuildResetPlan(e.graph, e.state, record.Resource.String())
			if err != nil {
				return err
			}
			if len(reset.Actions) != 1 {
				return ErrActionUncertain
			}
			change = reset.Actions[0]
		}
		if record.Action != controlplane.PlanActionDelete && e.graph.Get(record.Resource) == nil {
			return ErrPlanStale
		}
		plan.Actions = append(plan.Actions, change)
	}
	if count == 0 {
		return fmt.Errorf("%w: no durable actions for run", ErrConvergenceUnsupported)
	}
	original := e.actions
	e.actions = &runRecoveryRepository{ActionRepository: original, runID: runID}
	defer func() { e.actions = original }()
	return e.RecoverActions(ctx, plan)
}

type runRecoveryRepository struct {
	ActionRepository
	runID string
}

func (r *runRecoveryRepository) LoadActions(ctx context.Context, topology string) ([]ActionRecord, error) {
	records, err := r.ActionRepository.LoadActions(ctx, topology)
	if err != nil {
		return nil, err
	}
	out := []ActionRecord{}
	for _, record := range records {
		if record.RunID != r.runID {
			if !ActionSettled(record) {
				return nil, ErrActionUncertain
			}
			continue
		}
		out = append(out, record)
	}
	return out, nil
}
