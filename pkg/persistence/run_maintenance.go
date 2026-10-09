package persistence

// ValidateRunMaintenance consumes observations read while authority is locked
// through Run publication. It validates the complete records, never a guessed
// UID. Before lifecycle initialization, an active barrier conservatively blocks
// admission because the new Run's ownership cannot yet be established.
func ValidateRunMaintenance(authorityRaw, lifecycleRaw []byte, topology string) error {
	if !topologyName.MatchString(topology) {
		return ErrInvalidRecord
	}
	authority, err := decodeExecutionAuthority(authorityRaw)
	if err != nil {
		return err
	}
	if lifecycleRaw == nil {
		for _, barrier := range authority.Maintenance {
			if !barrier.Released {
				return ErrConflict
			}
		}
		return nil
	}
	lifecycle, err := decodeAggregate(lifecycleRaw)
	if err != nil {
		return err
	}
	if err = validateAggregate(lifecycle, topology); err != nil {
		return err
	}
	if authority.maintenanceBlocks(lifecycle.State.TopologyUID) {
		return ErrConflict
	}
	return nil
}
