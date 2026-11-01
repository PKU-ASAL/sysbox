package persistence

import (
	"encoding/json"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// A failed delivery is not execution evidence. Once retired, no later grant
// can be issued; the same authority transaction checks all historical grants.
func PrepareUnissuedGuestExecutionFailure(id string, raw, authority []byte, lookup func(string) (*CommandRecord, error)) (*GuestExecutionMutation, error) {
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	if raw == nil {
		return nil, ErrNotFound
	}
	x, err := decodeGuestRecord(id, raw, executionCodec)
	if err != nil {
		return nil, err
	}
	if x.CommandID == "" || x.CancelRequested || x.CreatedAt.IsZero() {
		return nil, ErrConflict
	}
	source, err := lookup(x.CommandID)
	if err != nil {
		return nil, err
	}
	if source == nil || !source.Retired || source.Command.ID != x.CommandID || source.Command.AgentID != x.AgentID || source.Command.Type != "guest_execution" || source.Command.EndedAt.IsZero() {
		return nil, ErrConflict
	}
	switch source.Command.Status {
	case controlplane.AgentCommandStatusFailed, controlplane.AgentCommandStatusDenied, controlplane.AgentCommandStatusCancelled:
	default:
		return nil, ErrConflict
	}
	admitted, err := originalGuestExecutionAdmission(raw, x, source)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, ErrConflict
	}
	a, err := decodeExecutionAuthority(authority)
	if err != nil {
		return nil, err
	}
	if now := time.Now().UTC(); now.After(a.ObservedAt) {
		a.ObservedAt = now
	}
	next := raw
	if !guestTerminal(x.Status) {
		if !a.hasCommandExecutionHistory(x.AgentID, x.CommandID) {
			if x.Version == math.MaxInt64 {
				return nil, ErrConflict
			}
			setUnstartedGuestResult(&x, controlplane.GuestExecutionFailed, "delivery failed before execution authorization", a.ObservedAt)
			x.Version++
			next, err = encodeExecution(x)
			if err != nil {
				return nil, err
			}
			next, err = preserveGuestDispatch(raw, next)
			if err != nil {
				return nil, err
			}
		} else {
			for key, g := range a.Grants {
				if g.AgentID == x.AgentID && g.CommandID == x.CommandID && g.State != controlplane.GrantStopped {
					g.State = controlplane.GrantRevoked
					a.Grants[key] = g
				}
			}
		}
	}
	nextAuthority, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return &GuestExecutionMutation{Execution: x, Record: next, Authority: nextAuthority}, nil
}
