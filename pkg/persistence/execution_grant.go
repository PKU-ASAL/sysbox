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

// Version 3 adds immutable externally attested fencing evidence. Older writers
// must reject this ledger rather than discard its pinned requests and receipts.
const executionAuthorityVersion = 3

// One authority ledger serializes session changes and overlapping scopes in
// the same storage transaction. Epoch tombstones are never deleted. This is
// intentionally a single writer boundary until a sharded authority can preserve
// topology-versus-resource exclusion and session CAS in one transaction.
type executionAuthority struct {
	Fences       map[string]HostFenceRecord             `json:"host_fences,omitempty"`
	Maintenance  map[string]MaintenanceBarrier          `json:"maintenance,omitempty"`
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

func (r *ExecutionGrantRepository) ListActiveGrants(ctx context.Context) ([]controlplane.ExecutionGrant, error) {
	out := []controlplane.ExecutionGrant{}
	err := r.read(ctx, func(a *executionAuthority) error {
		for _, g := range a.Grants {
			if g.State != controlplane.GrantStopped {
				out = append(out, g)
			}
		}
		return nil
	})
	return out, err
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
		return fn(&executionAuthority{Version: executionAuthorityVersion, Sessions: map[string]controlplane.AgentSession{}, Grants: map[string]controlplane.ExecutionGrant{}})
	}
	if err != nil {
		return err
	}
	a, err := decodeExecutionAuthority(raw)
	if err != nil {
		return err
	}
	return fn(a)
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
		decoded, err := decodeExecutionAuthority(raw)
		if err != nil {
			return nil, err
		}
		a := *decoded
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

func isRunChildOperation(operation string) bool {
	return operation == "guest_task" || controlplane.IsGuestSSHAccessOperation(operation)
}

func nestedRunGrantPair(a, b controlplane.ExecutionGrant) bool {
	if a.Scope.ResourceUID == "" && b.Scope.ResourceUID != "" {
		return controlplane.IsRunExecutionOperation(a.Operation) && isRunChildOperation(b.Operation) && b.ParentEpoch == a.Epoch && a.AgentID == b.AgentID && a.SessionID == b.SessionID && a.CommandID == b.CommandID && a.Attempt == b.Attempt
	}
	if b.Scope.ResourceUID == "" && a.Scope.ResourceUID != "" {
		return nestedRunGrantPair(b, a)
	}
	return false
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
	return a.ParentEpoch == b.ParentEpoch && a.Scope == b.Scope && a.TargetBootID == b.TargetBootID && a.AgentID == b.AgentID && a.SessionID == b.SessionID && a.ActionID == b.ActionID && a.CommandID == b.CommandID && a.Attempt == b.Attempt && a.Operation == b.Operation && a.Digest == b.Digest
}
func sameGrantEpoch(a, b controlplane.ExecutionGrant) bool {
	return a.Epoch == b.Epoch && sameGrant(a, b)
}
func validTTL(ttl time.Duration) bool { return ttl > 0 && ttl <= MaxExecutionGrantTTL }

func (r *ExecutionGrantRepository) Issue(ctx context.Context, request controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	if !validGrant(request) || !validTTL(ttl) || request.ParentEpoch != 0 {
		return nil, ErrInvalidRecord
	}
	var result controlplane.ExecutionGrant
	err := r.update(ctx, func(a *executionAuthority) error {
		if a.hostFenceBlocks(request.AgentID) || a.maintenanceBlocks(request.Scope.TopologyUID) {
			return ErrConflict
		}
		if a.Sessions[request.AgentID].ID != request.SessionID {
			return ErrConflict
		}
		if !request.ExpiresAt.IsZero() && !request.ExpiresAt.After(a.ObservedAt) {
			return ErrConflict
		}
		// Match the existing scope before scanning overlapping child scopes;
		// map iteration order must not change exact parent retry behavior.
		if g, ok := a.Grants[grantScopeKey(request.Scope)]; ok && sameGrant(g, request) && g.State == controlplane.GrantActive && a.ObservedAt.Before(g.ExpiresAt) {
			result = g
			return nil
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

// DeriveGuestTask creates a durable resource-scoped guest grant under an
// active host-owned Run grant. The parent topology grant remains active while
// child resource grants occupy their own exclusion keys.
func (r *ExecutionGrantRepository) DeriveGuestTask(ctx context.Context, parent, child controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	if parent.AgentID == "" || parent.SessionID == "" || !controlplane.IsRunExecutionOperation(parent.Operation) || !isRunChildOperation(child.Operation) || child.Scope.TopologyUID != parent.Scope.TopologyUID || child.Scope.ResourceUID == "" || child.Scope.Generation < 1 || child.AgentID != parent.AgentID || child.SessionID != parent.SessionID || child.CommandID != parent.CommandID || child.Attempt != parent.Attempt || child.ActionID == "" || child.Digest == "" || child.TargetBootID == "" || !validTTL(ttl) {
		return nil, ErrInvalidRecord
	}
	var result controlplane.ExecutionGrant
	err := r.update(ctx, func(a *executionAuthority) error {
		current, ok := a.Grants[grantScopeKey(parent.Scope)]
		if !ok || parent.Scope.ResourceUID != "" || current.Epoch != parent.Epoch || a.Sessions[parent.AgentID].ID != parent.SessionID || current.State != controlplane.GrantActive || !sameGrant(current, parent) || !a.ObservedAt.Before(current.ExpiresAt) || !a.ObservedAt.Before(parent.ExpiresAt) {
			return ErrConflict
		}
		if child.ParentEpoch != 0 && child.ParentEpoch != current.Epoch || !child.ExpiresAt.IsZero() && !child.ExpiresAt.After(a.ObservedAt) {
			return ErrConflict
		}
		child.ParentEpoch = current.Epoch
		// A completed task is never reissued, even after another task has
		// replaced its resource slot. Host-stop tombstones preserve this fact.
		for _, stopped := range a.Stops {
			if sameGrant(stopped.Grant, child) {
				return ErrConflict
			}
		}
		for key, existing := range a.Grants {
			if key == grantScopeKey(parent.Scope) || existing.State == controlplane.GrantStopped {
				continue
			}
			if scopesOverlap(existing.Scope, child.Scope) {
				if sameGrant(existing, child) && existing.State == controlplane.GrantActive && a.ObservedAt.Before(existing.ExpiresAt) && !existing.ExpiresAt.After(parent.ExpiresAt) {
					result = existing
					return nil
				}
				return ErrConflict
			}
		}
		if a.Epoch == math.MaxUint64 {
			return ErrConflict
		}
		a.Epoch++
		result = child
		result.Epoch = a.Epoch
		result.ExpiresAt = a.ObservedAt.Add(ttl)
		if !child.ExpiresAt.IsZero() && child.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = child.ExpiresAt
		}
		if current.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = current.ExpiresAt
		}
		if parent.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = parent.ExpiresAt
		}
		result.State = controlplane.GrantActive
		result.StopReceipt = ""
		a.Grants[grantScopeKey(result.Scope)] = result
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

func activeParent(a *executionAuthority, child controlplane.ExecutionGrant) (controlplane.ExecutionGrant, error) {
	parent, ok := a.Grants[grantScopeKey(controlplane.ExecutionScope{TopologyUID: child.Scope.TopologyUID})]
	if !ok || child.ParentEpoch == 0 || !nestedRunGrantPair(parent, child) || parent.State != controlplane.GrantActive || a.Sessions[parent.AgentID].ID != parent.SessionID || !a.ObservedAt.Before(parent.ExpiresAt) || child.ExpiresAt.After(parent.ExpiresAt) {
		return parent, ErrConflict
	}
	return parent, nil
}

// RenewDerivedGuestTask renews only the exact child of a still-live parent.
// Parent renewal must precede child renewal; neither operation resurrects expiry.
func (r *ExecutionGrantRepository) RenewDerivedGuestTask(ctx context.Context, parent, child controlplane.ExecutionGrant, ttl time.Duration) (*controlplane.ExecutionGrant, error) {
	if !validTTL(ttl) {
		return nil, ErrInvalidRecord
	}
	var result controlplane.ExecutionGrant
	err := r.update(ctx, func(a *executionAuthority) error {
		g, err := currentGrant(a, child)
		if err != nil {
			return err
		}
		p, err := activeParent(a, g)
		if err != nil || !sameGrant(p, parent) || p.Epoch != parent.Epoch || g.State != controlplane.GrantActive || !a.ObservedAt.Before(g.ExpiresAt) || !a.ObservedAt.Before(parent.ExpiresAt) || g.ExpiresAt.After(parent.ExpiresAt) {
			return ErrConflict
		}
		deadline := a.ObservedAt.Add(ttl)
		if p.ExpiresAt.Before(deadline) {
			deadline = p.ExpiresAt
		}
		if parent.ExpiresAt.Before(deadline) {
			deadline = parent.ExpiresAt
		}
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
		if g.ParentEpoch != 0 {
			_, err = activeParent(a, g)
			return err
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
		if g.ParentEpoch != 0 || a.Sessions[g.AgentID].ID != g.SessionID || g.State != controlplane.GrantActive || !now.Before(g.ExpiresAt) {
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
		// Discovery consumes durable revoked state, not merely the online
		// rejection inherited from a revoked parent. Publish both atomically.
		for key, child := range a.Grants {
			if child.ParentEpoch == g.Epoch && child.State != controlplane.GrantStopped {
				child.State = controlplane.GrantRevoked
				a.Grants[key] = child
			}
		}
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

// ApplyHostStopAuthority is the raw-transaction form used by operation
// adapters that commit their result and authority release in one backend
// transaction. It has the same idempotency and successor fencing semantics as
// AcceptHostStop.
func ApplyHostStopAuthority(raw []byte, proof controlplane.ExecutionGrant, receipt string) ([]byte, error) {
	a, err := decodeExecutionAuthority(raw)
	if err != nil {
		return nil, err
	}
	if receipt == "" {
		return nil, ErrInvalidRecord
	}
	if prior, ok := a.Stops[proof.Epoch]; ok {
		if sameGrantEpoch(prior.Grant, proof) && prior.Receipt == receipt {
			return raw, nil
		}
		return nil, ErrConflict
	}
	current, ok := a.Grants[grantScopeKey(proof.Scope)]
	if !ok || !sameGrantEpoch(current, proof) || current.State == controlplane.GrantStopped {
		return nil, ErrConflict
	}
	for _, child := range a.Grants {
		if child.ParentEpoch == current.Epoch && child.State != controlplane.GrantStopped {
			return nil, ErrConflict
		}
	}
	current.State, current.StopReceipt = controlplane.GrantStopped, receipt
	a.Grants[grantScopeKey(current.Scope)] = current
	if a.Stops == nil {
		a.Stops = map[uint64]stoppedExecutionGrant{}
	}
	a.Stops[current.Epoch] = stoppedExecutionGrant{Grant: current, Receipt: receipt}
	return json.Marshal(a)
}

// PrepareHostStop freezes a host-confirmed parent before committing its Run
// outcome. Revocation retains exclusion while preventing new child authority;
// AcceptHostStop releases the scope only after the canonical commit succeeds.
func (r *ExecutionGrantRepository) PrepareHostStop(ctx context.Context, proof controlplane.ExecutionGrant, receipt string) error {
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
		for _, child := range a.Grants {
			if child.ParentEpoch == g.Epoch && child.State != controlplane.GrantStopped {
				return ErrConflict
			}
		}
		g.State = controlplane.GrantRevoked
		a.Grants[grantScopeKey(g.Scope)] = g
		return nil
	})
}

func (r *ExecutionGrantRepository) ValidateHostStop(ctx context.Context, proof controlplane.ExecutionGrant, receipt string) error {
	return r.read(ctx, func(a *executionAuthority) error {
		if prior, ok := a.Stops[proof.Epoch]; ok {
			if sameGrant(prior.Grant, proof) && prior.Receipt == receipt {
				return nil
			}
			return ErrConflict
		}
		_, err := currentGrant(a, proof)
		return err
	})
}

// RequireHostStopped reads committed historical stop evidence only. Active or
// revoked grants cannot establish that their provider effects have stopped.
func (r *ExecutionGrantRepository) RequireHostStopped(ctx context.Context, proof controlplane.ExecutionGrant, receipt string) error {
	if receipt == "" {
		return ErrInvalidRecord
	}
	return r.read(ctx, func(a *executionAuthority) error {
		prior, ok := a.Stops[proof.Epoch]
		if !ok || !sameGrant(prior.Grant, proof) || prior.Receipt != receipt {
			return ErrConflict
		}
		return nil
	})
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
		for _, child := range a.Grants {
			if child.ParentEpoch == g.Epoch && child.State != controlplane.GrantStopped {
				return ErrConflict
			}
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
