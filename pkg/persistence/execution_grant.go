package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
)

const executionAuthorityKind guestKind = "sysbox_execution_authority"
const MaxExecutionGrantTTL = controlplane.ExecutionGrantMaxTTL

// One authority ledger serializes session changes and overlapping scopes in
// the same storage transaction. Epoch tombstones are never deleted. This is
// intentionally a single writer boundary until a sharded authority can preserve
// topology-versus-resource exclusion and session CAS in one transaction.
type executionAuthority struct {
	Stops        map[uint64]stoppedExecutionGrant       `json:"stops,omitempty"`
	Observations map[string]ExecutionObservation        `json:"observations"`
	Version      int                                    `json:"version"`
	Epoch        uint64                                 `json:"epoch"`
	ObservedAt   time.Time                              `json:"observed_at"`
	Sessions     map[string]controlplane.AgentSession   `json:"sessions"`
	Grants       map[string]controlplane.ExecutionGrant `json:"grants"`
}
type stoppedExecutionGrant struct {
	Grant   controlplane.ExecutionGrant `json:"grant"`
	Receipt string                      `json:"receipt"`
}

// InspectGrant is discovery only, including expired grants and retired sessions.
func (r *ExecutionGrantRepository) InspectGrant(ctx context.Context, agent string, scope controlplane.ExecutionScope) (*controlplane.ExecutionGrant, error) {
	var result controlplane.ExecutionGrant
	err := r.read(ctx, func(a *executionAuthority) error {
		g, ok := a.Grants[grantScopeKey(scope)]
		if !ok || g.AgentID != agent {
			return ErrNotFound
		}
		if g.Scope != scope {
			return ErrConflict
		}
		result = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

type ExecutionGrantRepository struct {
	records guestRecords
	storage ExecutionGrantStorage
	now     func() time.Time
}

// ExecutionGrantStorage serializes the authority ledger. API adapters can
// additionally lock and validate the source command in this same transaction.
type ExecutionGrantStorage interface {
	ReadExecutionAuthority(context.Context) ([]byte, error)
	UpdateExecutionAuthority(context.Context, func([]byte) ([]byte, error)) error
}

func (b executionGrantRecords) ReadExecutionAuthority(ctx context.Context) ([]byte, error) {
	return b.records.get(ctx, executionAuthorityKind, "authority")
}

func (r *ExecutionGrantRepository) read(ctx context.Context, fn func(*executionAuthority) error) error {
	raw, err := r.storage.ReadExecutionAuthority(ctx)
	if errors.Is(err, ErrNotFound) {
		return fn(&executionAuthority{Version: 1, Sessions: map[string]controlplane.AgentSession{}, Grants: map[string]controlplane.ExecutionGrant{}})
	}
	if err != nil {
		return err
	}
	var a executionAuthority
	if json.Unmarshal(raw, &a) != nil || a.Sessions == nil || a.Grants == nil || a.ObservedAt.IsZero() {
		return ErrCorruptRecord
	}
	if a.Version != 1 {
		return ErrUnsupportedVersion
	}
	return fn(&a)
}

func NewExecutionGrantRepository(storage ExecutionGrantStorage) *ExecutionGrantRepository {
	return &ExecutionGrantRepository{storage: storage, now: func() time.Time { return time.Now().UTC() }}
}

// ObserveTime advances the durable clock floor even when a compound operation
// rejects its source command before reaching a grant transition.
func (r *ExecutionGrantRepository) ObserveTime(ctx context.Context) error {
	return r.update(ctx, func(*executionAuthority) error { return nil })
}

type executionGrantRecords struct{ records guestRecords }

func (b executionGrantRecords) UpdateExecutionAuthority(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	return b.records.update(ctx, executionAuthorityKind, "authority", fn)
}

func newExecutionGrantRepository(records guestRecords) *ExecutionGrantRepository {
	r := NewExecutionGrantRepository(executionGrantRecords{records: records})
	r.records = records
	return r
}

func (r *ExecutionGrantRepository) update(ctx context.Context, fn func(*executionAuthority) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var transitionErr error
	err := r.storage.UpdateExecutionAuthority(ctx, func(raw []byte) ([]byte, error) {
		a := executionAuthority{Version: 1, Sessions: map[string]controlplane.AgentSession{}, Grants: map[string]controlplane.ExecutionGrant{}}
		if raw != nil {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, ErrCorruptRecord
			}
			if a.Version != 1 {
				return nil, ErrUnsupportedVersion
			}
		}
		if a.Sessions == nil || a.Grants == nil {
			return nil, ErrCorruptRecord
		}
		if raw != nil && a.ObservedAt.IsZero() {
			return nil, ErrCorruptRecord
		}
		for key, s := range a.Sessions {
			if key == "" || key != s.AgentID || s.ID == "" || s.IncarnationID == "" || s.CreatedAt.IsZero() {
				return nil, ErrCorruptRecord
			}
		}
		for epoch, stop := range a.Stops {
			if epoch == 0 || epoch > a.Epoch || stop.Grant.Epoch != epoch || !validGrant(stop.Grant) || stop.Grant.State != controlplane.GrantStopped || stop.Receipt == "" || stop.Grant.StopReceipt != stop.Receipt {
				return nil, ErrCorruptRecord
			}
		}
		for key, g := range a.Grants {
			if key != grantScopeKey(g.Scope) || !validGrant(g) || g.Epoch == 0 || g.Epoch > a.Epoch || g.ExpiresAt.IsZero() {
				return nil, ErrCorruptRecord
			}
			switch g.State {
			case controlplane.GrantActive, controlplane.GrantRevoked:
				if g.StopReceipt != "" {
					return nil, ErrCorruptRecord
				}
			case controlplane.GrantStopped:
				if g.StopReceipt == "" {
					return nil, ErrCorruptRecord
				}
			default:
				return nil, ErrCorruptRecord
			}
			if g.State != controlplane.GrantStopped {
				for otherKey, other := range a.Grants {
					if otherKey != key && other.State != controlplane.GrantStopped && scopesOverlap(g.Scope, other.Scope) {
						return nil, ErrCorruptRecord
					}
				}
			}
		}
		if now := r.now(); now.After(a.ObservedAt) {
			a.ObservedAt = now
		}
		// Even a rejected expired proof advances the durable clock floor. A
		// later wall-clock rollback must never resurrect that authorization.
		before, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		if transitionErr = fn(&a); transitionErr != nil {
			return before, nil
		}
		return json.Marshal(a)
	})
	if err != nil {
		return err
	}
	return transitionErr
}

// OpenSession is authenticated registration CAS. A delayed old registration
// cannot overwrite the new session. The same process incarnation retry is
// idempotent. Incarnation is a fresh random nonce on every Agent start, not the
// OS boot ID (which is shared by multiple Agent restarts).
func (r *ExecutionGrantRepository) OpenSession(ctx context.Context, agent, boot, expected string) (*controlplane.AgentSession, error) {
	if agent == "" || boot == "" {
		return nil, ErrInvalidRecord
	}
	var result controlplane.AgentSession
	err := r.update(ctx, func(a *executionAuthority) error {
		current, exists := a.Sessions[agent]
		if exists && current.IncarnationID == boot {
			result = current
			return nil
		}
		if exists && current.ID != expected || !exists && expected != "" {
			return ErrConflict
		}
		result = controlplane.AgentSession{AgentID: agent, ID: uuid.NewString(), IncarnationID: boot, CreatedAt: a.ObservedAt}
		a.Sessions[agent] = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func validGrant(g controlplane.ExecutionGrant) bool {
	return g.Scope.TopologyUID != "" && (g.Scope.ResourceUID == "" && g.Scope.Generation == 0 || g.Scope.ResourceUID != "" && g.Scope.Generation > 0) && g.AgentID != "" && g.SessionID != "" && g.ActionID != "" && g.CommandID != "" && g.Attempt > 0 && g.Operation != "" && g.Digest != ""
}
func grantScopeKey(s controlplane.ExecutionScope) string {
	// Generations of a resource share an exclusion domain. Replacing a resource
	// cannot let old-generation in-flight effects overlap the replacement.
	raw, _ := json.Marshal([]string{s.TopologyUID, s.ResourceUID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func scopesOverlap(a, b controlplane.ExecutionScope) bool {
	return a.TopologyUID == b.TopologyUID && (a.ResourceUID == "" || b.ResourceUID == "" || a.ResourceUID == b.ResourceUID)
}
func sameGrant(a, b controlplane.ExecutionGrant) bool {
	return a.Scope == b.Scope && a.TargetBootID == b.TargetBootID && a.AgentID == b.AgentID && a.SessionID == b.SessionID && a.ActionID == b.ActionID && a.CommandID == b.CommandID && a.Attempt == b.Attempt && a.Operation == b.Operation && a.Digest == b.Digest
}
func validTTL(ttl time.Duration) bool { return ttl > 0 && ttl <= MaxExecutionGrantTTL }

func (r *ExecutionGrantRepository) Issue(ctx context.Context, request controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	if !validGrant(request) || !validTTL(ttl) {
		return nil, ErrInvalidRecord
	}
	var result controlplane.ExecutionGrant
	err := r.update(ctx, func(a *executionAuthority) error {
		if a.Sessions[request.AgentID].ID != request.SessionID {
			return ErrConflict
		}
		if !request.ExpiresAt.IsZero() && !request.ExpiresAt.After(a.ObservedAt) {
			return ErrConflict
		}
		for _, g := range a.Grants {
			if !scopesOverlap(g.Scope, request.Scope) || g.State == controlplane.GrantStopped {
				continue
			}
			if sameGrant(g, request) && g.State == controlplane.GrantActive && a.ObservedAt.Before(g.ExpiresAt) {
				result = g
				return nil
			}
			return ErrConflict
		}
		if a.Epoch == math.MaxUint64 {
			return ErrConflict
		}
		a.Epoch++
		result = request
		result.Epoch = a.Epoch
		result.ExpiresAt = a.ObservedAt.Add(ttl)
		if !request.ExpiresAt.IsZero() && request.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = request.ExpiresAt
		}
		result.State = controlplane.GrantActive
		result.StopReceipt = ""
		a.Grants[grantScopeKey(request.Scope)] = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func currentGrant(a *executionAuthority, proof controlplane.ExecutionGrant) (controlplane.ExecutionGrant, error) {
	g, ok := a.Grants[grantScopeKey(proof.Scope)]
	if !ok {
		return g, ErrNotFound
	}
	if !sameGrant(g, proof) || g.Epoch != proof.Epoch {
		return g, ErrConflict
	}
	return g, nil
}

// Authorize is an online authority check. A host must still journal/deduplicate
// effects and enforce a monotonic local deadline; this check alone is not a gate.
func (r *ExecutionGrantRepository) Authorize(ctx context.Context, proof controlplane.ExecutionGrant) error {
	return r.update(ctx, func(a *executionAuthority) error {
		g, err := currentGrant(a, proof)
		if err != nil {
			return err
		}
		if a.Sessions[g.AgentID].ID != g.SessionID || g.State != controlplane.GrantActive || !a.ObservedAt.Before(g.ExpiresAt) {
			return ErrConflict
		}
		return nil
	})
}
func (r *ExecutionGrantRepository) Renew(ctx context.Context, proof controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	if !validTTL(ttl) {
		return nil, ErrInvalidRecord
	}
	var result controlplane.ExecutionGrant
	err := r.update(ctx, func(a *executionAuthority) error {
		g, err := currentGrant(a, proof)
		if err != nil {
			return err
		}
		now := a.ObservedAt
		if a.Sessions[g.AgentID].ID != g.SessionID || g.State != controlplane.GrantActive || !now.Before(g.ExpiresAt) {
			return ErrConflict
		}
		deadline := now.Add(ttl)
		if deadline.After(g.ExpiresAt) {
			g.ExpiresAt = deadline
		}
		a.Grants[grantScopeKey(g.Scope)] = g
		result = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (r *ExecutionGrantRepository) Revoke(ctx context.Context, proof controlplane.ExecutionGrant) error {
	return r.update(ctx, func(a *executionAuthority) error {
		g, err := currentGrant(a, proof)
		if err != nil {
			return err
		}
		if g.State == controlplane.GrantStopped {
			return nil
		}
		g.State = controlplane.GrantRevoked
		a.Grants[grantScopeKey(g.Scope)] = g
		return nil
	})
}

// AcknowledgeStop is reserved for authenticated resource-host receipts, not
// worker completion reports. Expiry/revocation alone never proves quiescence.
func (r *ExecutionGrantRepository) AcknowledgeStop(ctx context.Context, proof controlplane.ExecutionGrant, receipt string) error {
	return r.acknowledgeStop(ctx, proof, receipt, false)
}

// AcceptHostStop atomically retires a grant using authenticated host evidence.
// Exact replay remains valid after a successor has replaced the scope record.
func (r *ExecutionGrantRepository) AcceptHostStop(ctx context.Context, proof controlplane.ExecutionGrant, receipt string) error {
	return r.acknowledgeStop(ctx, proof, receipt, true)
}
func (r *ExecutionGrantRepository) acknowledgeStop(ctx context.Context, proof controlplane.ExecutionGrant, receipt string, hostConfirmed bool) error {
	if receipt == "" {
		return ErrInvalidRecord
	}
	return r.update(ctx, func(a *executionAuthority) error {
		if prior, ok := a.Stops[proof.Epoch]; ok {
			if sameGrant(prior.Grant, proof) && prior.Receipt == receipt {
				return nil
			}
			return ErrConflict
		}
		g, err := currentGrant(a, proof)
		if err != nil {
			return err
		}
		if g.State == controlplane.GrantStopped {
			if g.StopReceipt == receipt {
				return nil
			}
			return ErrConflict
		}
		if g.State != controlplane.GrantRevoked && !hostConfirmed {
			return ErrConflict
		}
		g.State = controlplane.GrantStopped
		g.StopReceipt = receipt
		a.Grants[grantScopeKey(g.Scope)] = g
		if a.Stops == nil {
			a.Stops = map[uint64]stoppedExecutionGrant{}
		}
		a.Stops[g.Epoch] = stoppedExecutionGrant{Grant: g, Receipt: receipt}
		return nil
	})
}
