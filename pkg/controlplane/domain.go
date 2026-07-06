package controlplane

import (
	"fmt"
	"time"
)

const (
	PlanStatusPlanned = "planned"

	AgentStatusOnline      = "online"
	AgentStatusOffline     = "offline"
	AgentStatusDisabled    = "disabled"
	AgentStatusQuarantined = "quarantined"

	AgentCommandStatusQueued    = "queued"
	AgentCommandStatusLeased    = "leased"
	AgentCommandStatusDelivered = "delivered"
	AgentCommandStatusRunning   = "running"
	AgentCommandStatusCompleted = "completed"
	AgentCommandStatusFailed    = "failed"
	AgentCommandStatusDenied    = "denied"
	AgentCommandStatusCancelled = "cancelled"

	ConsoleSessionStatusQueued    = "queued"
	ConsoleSessionStatusRunning   = "running"
	ConsoleSessionStatusClosed    = "closed"
	ConsoleSessionStatusFailed    = "failed"
	ConsoleSessionStatusCancelled = "cancelled"
	ConsoleSessionStatusDenied    = "denied"
	ConsoleSessionStatusLost      = "lost"

	NodeOperationStatusQueued  = "queued"
	NodeOperationStatusRunning = "running"
	NodeOperationStatusDone    = "done"
	NodeOperationStatusFailed  = "failed"
)

func (s RunStatus) IsActive() bool {
	switch s {
	case RunQueued, RunAssigned, RunRunning:
		return true
	default:
		return false
	}
}

func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunDone, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

func (s RunStatus) Rank() int {
	switch s {
	case RunQueued:
		return 1
	case RunAssigned:
		return 2
	case RunRunning:
		return 3
	case RunDone, RunFailed, RunCancelled:
		return 4
	default:
		return 0
	}
}

func (r Run) CanBeClaimedBy(agentID string, now time.Time) bool {
	if r.AgentID != agentID || r.Status != RunAssigned {
		return false
	}
	return r.LeaseUntil.IsZero() || !r.LeaseUntil.After(now)
}

func (r Run) CanRenewLease(agentID, owner string) bool {
	return r.AgentID == agentID && r.Status == RunRunning && r.LeaseOwner == owner
}

func (r *Run) MarkAssigned(agentID string, now time.Time) {
	r.AgentID = agentID
	r.Status = RunAssigned
	r.AssignedAt = now
}

func (r *Run) MarkRunning(owner string, ttl time.Duration, now time.Time) {
	r.Status = RunRunning
	r.LeaseOwner = owner
	r.LeaseUntil = now.Add(ttl)
	r.Attempt++
	if r.StartedAt.IsZero() || r.StartedAt.Equal(r.QueuedAt) {
		r.StartedAt = now
	}
}

func (r *Run) MarkFinished(err error, now time.Time) {
	r.EndedAt = now
	if err != nil {
		r.Status = RunFailed
		r.Err = err.Error()
		r.Recoverable = true
		return
	}
	r.Status = RunDone
	r.Err = ""
	r.Recoverable = false
}

func (r *Run) MarkLeaseExpired(now time.Time) {
	r.Status = RunFailed
	r.Err = "run lease expired"
	r.Recoverable = true
	r.EndedAt = now
}

func (a Agent) IsSchedulable() bool {
	return a.Status == AgentStatusOnline && !a.Disabled && !a.Quarantined
}

func (a Agent) IsBlocked() bool {
	return a.Disabled || a.Quarantined || a.Status == AgentStatusDisabled || a.Status == AgentStatusQuarantined
}

func AgentStatusForPolicy(disabled, quarantined bool) string {
	switch {
	case disabled:
		return AgentStatusDisabled
	case quarantined:
		return AgentStatusQuarantined
	default:
		return AgentStatusOnline
	}
}

func (p Plan) CanApply(currentRevision string, currentSerial int64) error {
	if p.Revision != "" && currentRevision != "" && p.Revision != currentRevision {
		return fmt.Errorf("plan revision %s is stale; current revision is %s", p.Revision, currentRevision)
	}
	if p.Status != "" && p.Status != PlanStatusPlanned {
		return fmt.Errorf("plan %s status is %s", p.ID, p.Status)
	}
	if p.StateSerial != currentSerial {
		return fmt.Errorf("plan state serial %d is stale; current serial is %d", p.StateSerial, currentSerial)
	}
	if len(p.Actions) == 0 {
		return fmt.Errorf("plan %s has no actions", p.ID)
	}
	return nil
}

func (c AgentCommand) IsPending() bool {
	switch c.Status {
	case "", AgentCommandStatusQueued, AgentCommandStatusDelivered:
		return true
	default:
		return false
	}
}

func (c AgentCommand) IsTerminal() bool {
	switch c.Status {
	case AgentCommandStatusCompleted, AgentCommandStatusFailed, AgentCommandStatusDenied, AgentCommandStatusCancelled:
		return true
	default:
		return false
	}
}

func (c AgentCommand) Leasable(now time.Time) bool {
	if !c.IsPending() {
		return false
	}
	return c.LeaseOwner == "" || c.LeaseUntil.IsZero() || !c.LeaseUntil.After(now)
}

func (c *AgentCommand) MarkLeased(owner string, ttl time.Duration, now time.Time) {
	c.Status = AgentCommandStatusLeased
	c.LeaseOwner = owner
	c.LeaseUntil = now.Add(ttl)
	c.Attempt++
}

func (c *AgentCommand) MarkDelivered(now time.Time) {
	c.Status = AgentCommandStatusDelivered
	c.Delivered = now
}
