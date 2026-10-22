package persistence

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// ValidateCommandLifecycle checks an already-locked aggregate. GC additionally
// binds the current reference digest and rejects retained resource identities.
// Callers issuing authority must retain their authority lock through commit; a preflight state
// read alone cannot prevent deletion from retiring this UID between the steps.
func ValidateCommandLifecycle(raw []byte, topology string, command controlplane.AgentCommand) error {
	if !topologyName.MatchString(topology) || command.TopologyUID == "" {
		return ErrInvalidRecord
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return err
	}
	if err = validateAggregate(a, topology); err != nil {
		return err
	}
	return validateCommandLifecycle(a, command)
}

func validateCommandLifecycle(a *aggregate, command controlplane.AgentCommand) error {
	if err := checkGCCommand(a, command); err != nil {
		return err
	}
	if a.State.TopologyUID != command.TopologyUID {
		return ErrConflict
	}
	for _, resource := range a.State.Resources {
		if !resource.RecoveryRequired() {
			continue
		}
		if command.Type == "run_assigned" && command.Run != nil {
			op := command.Run.Op
			if op == "" {
				op = command.Run.Operation
			}
			if op == "reset" || op == "destroy" {
				continue
			}
			return ErrConflict
		}
		if command.ResourceUID == "" || command.ResourceUID == resource.ResourceUID {
			return ErrConflict
		}
	}
	if ownershipPending(a) {
		req, err := controlplane.FromAdoptionCommand(command)
		if err != nil || req.Transfer == nil {
			return ErrConflict
		}
		target, ok := a.OwnershipTargets[req.ID]
		if !ok || target.CompletedHash != "" || evidenceHash(target.Plan) != evidenceHash(req.Transfer.Plan) {
			return ErrConflict
		}
	}
	if command.Type != "gc_delete" {
		return nil
	}
	return validateGCReferences(a, command)
}

func validateGCReferences(a *aggregate, command controlplane.AgentCommand) error {
	if a.State.TopologyUID != command.TopologyUID {
		return ErrConflict
	}
	inventory, err := gcReferences(a)
	if err != nil {
		return err
	}
	if command.GCInventoryDigest == "" || command.GCInventoryDigest != inventory.Digest || command.GCExternalID == "" || command.ResourceUID == "" || command.Generation < 1 {
		return ErrConflict
	}
	provider := ""
	switch command.GCKind {
	case "container", "network":
		provider = "docker"
	case "domain":
		provider = "libvirt"
	case "firecracker":
		provider = "firecracker"
	default:
		return ErrInvalidRecord
	}
	for _, ref := range inventory.References {
		// Any retained generation of this resource protects its identity. External
		// IDs are provider-scoped; matching them also protects adoption/rebinding.
		if ref.ResourceUID == command.ResourceUID || ref.Driver == provider && ref.ExternalID == command.GCExternalID {
			return ErrConflict
		}
	}
	return nil
}

// ValidateCommandLifecycle is the file-backed caller's aggregate lock boundary.
func (r *Repository) ValidateCommandLifecycle(ctx context.Context, topology string, command controlplane.AgentCommand) error {
	if command.TopologyUID == "" {
		return ErrInvalidRecord
	}
	return r.update(ctx, topology, func(a *aggregate) error {
		return validateCommandLifecycle(a, command)
	})
}
