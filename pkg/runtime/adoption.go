package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

func InspectAdoption(ctx context.Context, provider, name string, spec substrate.NodeSpec) (substrate.AdoptionEvidence, error) {
	p, err := driver.DefaultRegistry.RequireOwnershipAdoption(provider)
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	return p.InspectAdoption(ctx, name, spec)
}

// CommitAdoption journals before provider effects. Existing intents are only
// observed on retry, never blindly replayed after an uncertain ownership bind.
func CommitAdoption(ctx context.Context, actions ActionRepository, runID, topology string, addr address.Address, evidence substrate.AdoptionEvidence, resource state.Resource, identity substrate.OperationIdentity, expectedSerial int64) error {
	if actions == nil || runID == "" || topology == "" || addr.IsZero() || identity.Validate() != nil || evidence.ExternalID == "" || evidence.Fingerprint == "" {
		return fmt.Errorf("adoption requires complete immutable authorization")
	}
	if !resource.Address.Equal(addr) || resource.Driver != evidence.Provider || resource.ExternalID != evidence.ExternalID || resource.OwnerUID != identity.OwnerUID || resource.ResourceUID != identity.ResourceUID || resource.Generation != identity.Generation || identity.TopologyUID != identity.OwnerUID {
		return fmt.Errorf("adoption candidate identity conflict")
	}
	p, err := driver.DefaultRegistry.RequireOwnershipAdoption(evidence.Provider)
	if err != nil {
		return err
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(evidence.Provider)
	if err != nil {
		return err
	}
	st, err := actions.LoadState(ctx, topology)
	if err != nil {
		return err
	}
	if st.TopologyUID != identity.OwnerUID {
		return fmt.Errorf("adoption topology identity conflict")
	}
	// Creation timestamps are local persistence metadata, not provider proof;
	// exclude them so a lost completion can be retried with the same action ID.
	stableResource := resource
	stableResource.CreatedAt, stableResource.UpdatedAt = time.Time{}, time.Time{}
	raw, err := json.Marshal(struct {
		Run, Topology string
		Identity      substrate.OperationIdentity
		Evidence      substrate.AdoptionEvidence
		Resource      state.Resource
	}{runID, topology, identity, evidence, stableResource})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := fmt.Sprintf("%x", sum)
	action := ActionRecord{ID: "adopt-" + digest, Topology: topology, RunID: runID, Resource: addr, Action: controlplane.PlanActionAdopt, Status: ActionIntentCommitted, DesiredDigest: digest, OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation, StateSerial: expectedSerial, ExpectedSerial: expectedSerial, Candidate: &resource}
	journal, err := actions.LoadActions(ctx, topology)
	if err != nil {
		return err
	}
	recovering := false
	for _, old := range journal {
		if old.RunID == runID && old.DispositionID != "" {
			return ErrPlanStale
		}
		if old.ID == action.ID {
			if old.DesiredDigest != digest || old.OwnerUID != identity.OwnerUID || old.ResourceUID != identity.ResourceUID || old.Generation != identity.Generation || old.StateSerial != expectedSerial {
				return fmt.Errorf("adoption journal identity conflict")
			}
			if old.Status == ActionSucceeded {
				return nil
			}
			action = old
			recovering = true
			break
		}
	}
	if !recovering {
		if st.Meta.Serial != expectedSerial || st.FindResource(addr) != nil {
			return ErrPlanStale
		}
		if err := actions.CommitActionIntent(ctx, action); err != nil {
			return err
		}
		action.Revision = 1
	}
	ctx = substrate.WithOperationIdentity(ctx, identity)
	if !recovering {
		if _, err := p.ApplyAdoption(ctx, evidence); err != nil {
			action.Status = ActionUncertain
			action.Error = err.Error()
			if saveErr := actions.CommitActionOutcome(ctx, action, nil); saveErr != nil {
				return fmt.Errorf("adoption effect: %v; journal: %w", err, saveErr)
			}
			return err
		}
	}
	handle, err := p.ObserveAdoption(ctx, evidence)
	if err != nil {
		return err
	}
	if handle.ID != evidence.ExternalID || handle.Generation != identity.Generation {
		return fmt.Errorf("adoption observation identity conflict")
	}
	providerState, err := codec.MarshalProviderState(handle)
	if err != nil {
		return err
	}
	if err := resource.SetProviderState(providerState); err != nil {
		return err
	}
	action.Candidate = &resource
	action.Status = ActionSucceeded
	action.Error = ""
	action.EffectComplete = true
	action.ExpectedSerial = st.Meta.Serial
	logged := resourceLog(resource)
	patch := &StatePatch{Resource: addr.String(), Action: controlplane.PlanActionAdopt, Op: StatePatchUpsert, State: &logged}
	return actions.CommitActionOutcome(ctx, action, patch)
}

// CommitAdoptionEvidence commits a provider result already proven by the
// resource host. It records intent and outcome without re-running ApplyAdoption;
// host completion is the effect boundary, while this function is the atomic
// canonical state/journal patch.
func CommitAdoptionEvidence(ctx context.Context, actions ActionRepository, runID, topology string, addr address.Address, evidence substrate.AdoptionEvidence, resource state.Resource, identity substrate.OperationIdentity, expectedSerial int64) error {
	if atomic, ok := actions.(AdoptionEvidenceCommitter); ok {
		return atomic.CommitAdoptionEvidenceAtomic(ctx, runID, topology, addr, evidence, resource, identity, expectedSerial)
	}
	if actions == nil || runID == "" || topology == "" || addr.IsZero() || identity.Validate() != nil || evidence.ExternalID == "" || evidence.Fingerprint == "" {
		return fmt.Errorf("adoption evidence commit requires complete identity")
	}
	if !resource.Address.Equal(addr) || resource.Driver != evidence.Provider || resource.ExternalID != evidence.ExternalID || resource.OwnerUID != identity.OwnerUID || resource.ResourceUID != identity.ResourceUID || resource.Generation != identity.Generation || identity.TopologyUID != identity.OwnerUID {
		return fmt.Errorf("adoption evidence identity conflict")
	}
	st, err := actions.LoadState(ctx, topology)
	if err != nil {
		return err
	}
	if st.TopologyUID != identity.TopologyUID {
		return ErrPlanStale
	}
	// API receipt retries reconstruct these local timestamps. They are not
	// part of the authorized result; retain the first persisted values.
	stableResource := resource
	stableResource.CreatedAt, stableResource.UpdatedAt = time.Time{}, time.Time{}
	raw, err := json.Marshal(struct {
		Run, Topology string
		Identity      substrate.OperationIdentity
		Evidence      substrate.AdoptionEvidence
		Resource      state.Resource
	}{runID, topology, identity, evidence, stableResource})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := fmt.Sprintf("%x", sum)
	action := ActionRecord{ID: "adopt-" + digest, Topology: topology, RunID: runID, Resource: addr, Action: controlplane.PlanActionAdopt, Status: ActionIntentCommitted, DesiredDigest: digest, OwnerUID: identity.OwnerUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation, StateSerial: expectedSerial, ExpectedSerial: expectedSerial, Candidate: &resource}
	journal, err := actions.LoadActions(ctx, topology)
	if err != nil {
		return err
	}
	recovering := false
	for _, old := range journal {
		if old.RunID == runID && old.DispositionID != "" {
			return ErrPlanStale
		}
		if old.ID != action.ID {
			continue
		}
		if old.DesiredDigest != digest || old.StateSerial != expectedSerial || old.Topology != topology || old.RunID != runID || !old.Resource.Equal(addr) || old.Action != controlplane.PlanActionAdopt || old.OwnerUID != identity.OwnerUID || old.ResourceUID != identity.ResourceUID || old.Generation != identity.Generation || !old.EffectComplete || old.Candidate == nil {
			return ErrPlanStale
		}
		if old.Status == ActionSucceeded {
			return nil
		}
		action = old
		recovering = true
		break
	}
	if st.Meta.Serial != expectedSerial || st.FindResource(addr) != nil {
		return ErrPlanStale
	}
	if !recovering {
		if err := actions.CommitActionIntent(ctx, action); err != nil {
			return err
		}
		// Another identical completion may have won intent creation. Reuse
		// its exact candidate instead of overwriting first-commit timestamps.
		journal, err = actions.LoadActions(ctx, topology)
		if err != nil {
			return err
		}
		found := false
		for _, saved := range journal {
			if saved.ID != action.ID {
				continue
			}
			if saved.DesiredDigest != digest || saved.Candidate == nil {
				return ErrPlanStale
			}
			if saved.Status == ActionSucceeded {
				return nil
			}
			action = saved
			found = true
			break
		}
		if !found {
			return ErrActionUncertain
		}
	}
	action.Status = ActionSucceeded
	action.EffectComplete = true
	logged := resourceLog(*action.Candidate)
	patch := &StatePatch{Resource: addr.String(), Action: controlplane.PlanActionAdopt, Op: StatePatchUpsert, State: &logged}
	return actions.CommitActionOutcome(ctx, action, patch)
}
