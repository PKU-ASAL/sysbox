package state

import "fmt"

const recoveryRequirementKey = "intent_recovery_required"

// RequireRecovery records an incomplete local resource, never successful guest
// initialization. Ordinary effect admission must refuse it until an explicit
// reset succeeds or destruction removes the resource.
func (r *Resource) RequireRecovery(decision, resource string, generation int64) error {
	if decision == "" || resource == "" || generation < 1 || r.ResourceUID != resource || r.Generation != generation {
		return fmt.Errorf("invalid recovery requirement identity")
	}
	return r.SetRuntimeValue(recoveryRequirementKey, map[string]any{"decision": decision, "resource_uid": resource, "generation": generation})
}
func (r *Resource) RecoveryRequired() bool {
	values, err := r.RuntimeState()
	return err != nil || values[recoveryRequirementKey] != nil
}
func (r *Resource) ClearRecoveryRequirement() error {
	return r.DeleteRuntimeValue(recoveryRequirementKey)
}
