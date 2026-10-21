package persistence

import (
	"encoding/json"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// decodeExecutionAuthority is shared by reads, transitions and compound
// lifecycle commits. A syntactically valid stopped record without its evidence
// is corruption, never proof that the resource is safe to reclaim.
func decodeExecutionAuthority(raw []byte) (*executionAuthority, error) {
	if raw == nil {
		return &executionAuthority{Version: executionAuthorityVersion, Sessions: map[string]controlplane.AgentSession{}, Grants: map[string]controlplane.ExecutionGrant{}}, nil
	}
	a := &executionAuthority{}
	if json.Unmarshal(raw, a) != nil || a.Sessions == nil || a.Grants == nil || a.ObservedAt.IsZero() {
		return nil, ErrCorruptRecord
	}
	if a.Version != executionAuthorityVersion && a.Version != gcDispositionAuthorityVersion {
		return nil, ErrUnsupportedVersion
	}
	if err := validateMaintenance(a); err != nil {
		return nil, err
	}
	if err := validateHostFences(a); err != nil {
		return nil, err
	}
	for key, session := range a.Sessions {
		if key == "" || key != session.AgentID || session.ID == "" || session.IncarnationID == "" || session.CreatedAt.IsZero() {
			return nil, ErrCorruptRecord
		}
	}
	for epoch, stop := range a.Stops {
		if epoch == 0 || epoch > a.Epoch || stop.Grant.Epoch != epoch || !validGrant(stop.Grant) || stop.Grant.State != controlplane.GrantStopped || stop.Receipt == "" || stop.Grant.StopReceipt != stop.Receipt {
			return nil, ErrCorruptRecord
		}
	}
	for _, stop := range a.Stops {
		if strings.HasPrefix(stop.Receipt, "gc-fenced:") {
			matched := false
			for _, f := range a.Fences {
				if f.GCDisposition != nil && !f.GCDisposition.ReleasedAt.IsZero() && stop.Receipt == "gc-fenced:"+GCDispositionHash(f.GCDisposition.Proof) && sameGrantEpoch(stop.Grant, f.Request.Grant) {
					matched = true
				}
			}
			if !matched {
				return nil, ErrCorruptRecord
			}
		}
	}
	for epoch, stop := range a.Stops {
		if strings.HasPrefix(stop.Receipt, "fence-delete:") {
			matched := false
			for _, fence := range a.Fences {
				if fence.Request.Grant.Epoch == epoch && fence.Reconciliation != nil && fence.Reconciliation.Completed && stop.Receipt == fenceDeleteStopID(fence) {
					matched = true
				}
			}
			if !matched {
				return nil, ErrCorruptRecord
			}
		}
	}
	for key, grant := range a.Grants {
		if key != grantScopeKey(grant.Scope) || !validGrant(grant) || grant.Epoch == 0 || grant.Epoch > a.Epoch || grant.ExpiresAt.IsZero() {
			return nil, ErrCorruptRecord
		}
		switch grant.State {
		case controlplane.GrantActive, controlplane.GrantRevoked:
			if grant.StopReceipt != "" {
				return nil, ErrCorruptRecord
			}
		case controlplane.GrantStopped:
			if grant.StopReceipt == "" {
				return nil, ErrCorruptRecord
			}
		default:
			return nil, ErrCorruptRecord
		}
		if grant.State != controlplane.GrantStopped {
			for otherKey, other := range a.Grants {
				if otherKey != key && other.State != controlplane.GrantStopped && scopesOverlap(grant.Scope, other.Scope) && !nestedRunGrantPair(grant, other) {
					return nil, ErrCorruptRecord
				}
			}
		}
	}
	return a, nil
}
