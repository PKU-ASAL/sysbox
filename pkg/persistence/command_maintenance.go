package persistence

import "github.com/oslab/sysbox/pkg/controlplane"

// CommandNeedsMaintenanceAdmission distinguishes new work/delivery authority
// from evidence and stop requests needed to drain an already frozen topology.
func CommandNeedsMaintenanceAdmission(old *CommandRecord, next CommandRecord) bool {
	c := next.Command
	if next.Retired || c.IsTerminal() || c.Type == "cancel_command" || c.Type == "inventory" || (c.Type == controlplane.RunRecoveryCommand && c.RecoveryMode == controlplane.RunRecoveryObserve) {
		return false
	}
	if old == nil || old.Retired || old.Command.IsTerminal() {
		return true
	}
	p := old.Command
	return c.LeaseOwner != p.LeaseOwner || c.Attempt != p.Attempt || c.LeaseUntil.After(p.LeaseUntil) || (c.Status == controlplane.AgentCommandStatusDelivered && p.Status != c.Status)
}

// ValidateCommandMaintenance runs while storage holds authority through commit.
// Missing UID may locate a lifecycle for exclusion only, never grant ownership.
func ValidateCommandMaintenance(raw []byte, old *CommandRecord, next CommandRecord, checkTopology func(string) error) error {
	if !CommandNeedsMaintenanceAdmission(old, next) {
		return nil
	}
	authority, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	c := next.Command
	if c.TopologyUID != "" {
		if authority.maintenanceBlocks(c.TopologyUID) {
			return ErrConflict
		}
		return nil
	}
	names := []string{c.Operation.Topology}
	if c.Run != nil {
		names = append(names, c.Run.Topology)
	}
	if c.Execution != nil {
		names = append(names, c.Execution.Topology)
	}
	if c.FilePut != nil {
		names = append(names, c.FilePut.Topology)
	}
	if c.Session != nil {
		names = append(names, c.Session.Topology)
	}
	seen := map[string]bool{}
	for _, topology := range names {
		if topology == "" || seen[topology] {
			continue
		}
		if !topologyName.MatchString(topology) || checkTopology == nil {
			return ErrInvalidRecord
		}
		if err := checkTopology(topology); err != nil {
			return err
		}
		seen[topology] = true
	}
	if len(seen) == 0 {
		for _, barrier := range authority.Maintenance {
			if !barrier.Released {
				return ErrConflict
			}
		}
	}
	return nil
}
