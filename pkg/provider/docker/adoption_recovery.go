package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"strconv"
)

func (s *Substrate) InspectAdoptionRecovery(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("recovery identity required")
	}
	ins, err := s.cli.ContainerInspect(ctx, r.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if ins.ID != r.ExternalID {
		return substrate.NodeHandle{}, fmt.Errorf("immutable container identity mismatch")
	}
	labels, err := s.managedOwnershipLabels(ctx, ins)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err = verifyLabels(labels, operationLabels(id)); err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.adoptHandle(ctx, ins, id), nil
}

func (s *Substrate) InspectAdoptionCancellation(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeObservation, error) {
	zero := substrate.NodeObservation{}
	if !validAdoptionID(r.ExternalID) {
		return zero, fmt.Errorf("exact container ID required")
	}
	ins, err := s.cli.ContainerInspect(ctx, r.ExternalID)
	if errdefs.IsNotFound(err) {
		return substrate.NodeObservation{ExternalID: r.ExternalID}, nil
	}
	if err != nil {
		return zero, err
	}
	if ins.ID != r.ExternalID || ins.Config == nil || ins.State == nil || ins.State.Running {
		return zero, fmt.Errorf("cancellation requires inactive exact container")
	}
	labels, transferred, err := s.transferredLabels(ins)
	if err != nil {
		return zero, err
	}
	if !transferred {
		labels = ins.Config.Labels
	}
	observed := substrate.NodeObservation{Exists: true, ExternalID: r.ExternalID}
	if !transferred {
		raw, err := os.ReadFile(s.adoptionPath(r.ExternalID))
		if err == nil {
			var binding adoptionBinding
			if json.Unmarshal(raw, &binding) != nil || validateAdoptionBinding(binding, ins, binding.Identity) != nil {
				return zero, fmt.Errorf("adoption binding conflict")
			}
			labels = operationLabels(binding.Identity)
		} else if !os.IsNotExist(err) {
			return zero, err
		}
	}
	if labels["sysbox.owner_uid"] != "" || labels["sysbox.resource_uid"] != "" || labels["sysbox.managed"] != "" || labels["sysbox.generation"] != "" {
		generation, err := strconv.ParseInt(labels["sysbox.generation"], 10, 64)
		if err != nil || generation < 1 || labels["sysbox.owner_uid"] == "" || labels["sysbox.resource_uid"] == "" || labels["sysbox.topology_uid"] != labels["sysbox.owner_uid"] {
			return zero, fmt.Errorf("partial container ownership")
		}
		observed.OwnerUID, observed.ResourceUID, observed.Generation = labels["sysbox.owner_uid"], labels["sysbox.resource_uid"], generation
	}
	if _, err = controlplane.AdoptionCancellationResolution(r, observed); err != nil {
		return zero, err
	}
	return observed, nil
}
