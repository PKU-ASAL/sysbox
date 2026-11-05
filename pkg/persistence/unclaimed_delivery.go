package persistence

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

// RecoverUnclaimedDelivery only reopens transport for an assigned Run which
// never acquired a Run lease or execution grant. Called with Run, command and
// authority locked together; it does not authorize provider execution.
func RecoverUnclaimedDelivery(run *controlplane.Run, record *CommandRecord, authority []byte, now time.Time) (*CommandRecord, error) {
	if run == nil || record == nil || record.Retired {
		return nil, nil
	}
	c := record.Command
	if c.Type != "run_assigned" || c.Run == nil || c.IsTerminal() || c.LeaseUntil.IsZero() || c.LeaseUntil.After(now) {
		return nil, nil
	}
	if c.Status != controlplane.AgentCommandStatusAcknowledged && c.Status != controlplane.AgentCommandStatusRunning {
		return nil, nil
	}
	if run.Status != controlplane.RunAssigned || run.Attempt != 0 || run.LeaseOwner != "" || !run.LeaseUntil.IsZero() || run.HostCompletion != nil || !controlplane.SameRunIncarnation(*run, *c.Run) || run.AgentID != c.AgentID {
		return nil, nil
	}
	a, err := decodeExecutionAuthority(authority)
	if err != nil {
		return nil, err
	}
	for _, g := range a.Grants {
		if g.CommandID == c.ID || g.AgentID == c.AgentID && g.ActionID == c.ActionID {
			return nil, nil
		}
	}
	for _, s := range a.Stops {
		g := s.Grant
		if g.CommandID == c.ID || g.AgentID == c.AgentID && g.ActionID == c.ActionID {
			return nil, nil
		}
	}
	if err := ValidateMaintenanceWrite(authority, c.TopologyUID, c.AgentID); err != nil {
		return nil, err
	}
	c.Status = controlplane.AgentCommandStatusQueued
	c.LeaseOwner = ""
	c.LeaseUntil = time.Time{}
	c.AckedAt = time.Time{}
	return &CommandRecord{Command: c}, nil
}
