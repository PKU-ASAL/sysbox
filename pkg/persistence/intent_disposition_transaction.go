package persistence

import (
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const intentDispositionAuthorityVersion = 8

type FenceIntentReconciliation struct {
	Decision        controlplane.IntentDispositionDecision `json:"decision"`
	BeforeLifecycle string                                 `json:"before_lifecycle"`
	AfterLifecycle  string                                 `json:"after_lifecycle"`
	BeforeCommand   string                                 `json:"before_command"`
	OriginalRun     controlplane.Run                       `json:"original_run"`
	RunAfter        controlplane.Run                       `json:"run_after"`
	CommandAfter    CommandRecord                          `json:"command_after"`
	Observation     CommandRecord                          `json:"observation"`
	HostKey         []byte                                 `json:"host_key"`
	PreparedAt      time.Time                              `json:"prepared_at"`
	ReleasedAt      time.Time                              `json:"released_at,omitempty"`
}

type FenceIntentTransition struct {
	Authority []byte
	Lifecycle []byte
	Run       controlplane.Run
	Command   CommandRecord
}

// PrepareFenceIntent produces a write-ahead decision and exact postimages.
// Its caller must durably publish Authority first, then lifecycle and dispatch,
// and finish under the same admission/authority/lifecycle exclusion. It never
// contacts a provider or clears a barrier before the postimages are durable.
func PrepareFenceIntent(authority, lifecycle []byte, d controlplane.IntentDispositionDecision, run controlplane.Run, original, observation CommandRecord, key ed25519.PublicKey, now time.Time) (FenceIntentTransition, error) {
	var zero FenceIntentTransition
	auth, err := decodeExecutionAuthority(authority)
	if err != nil {
		return zero, err
	}
	f, ok := auth.Fences[d.FenceID]
	if !ok {
		return zero, ErrNotFound
	}
	a, err := decodeAggregate(lifecycle)
	if err != nil {
		return zero, err
	}
	if rec := f.IntentDisposition; rec != nil {
		if evidenceHash(rec.Decision) != evidenceHash(d) || !rec.ReleasedAt.IsZero() {
			return zero, ErrConflict
		}
		if evidenceHash(a) != rec.AfterLifecycle {
			if evidenceHash(a) != rec.BeforeLifecycle {
				return zero, ErrConflict
			}
			f.IntentDisposition = nil
			auth.Fences[d.FenceID] = f
			base, err := json.Marshal(auth)
			if err != nil {
				return zero, err
			}
			lifecycle, err = prepareIntentDispositionLifecycle(base, lifecycle, d, rec.OriginalRun, rec.Observation, rec.HostKey, now, rec.PreparedAt)
			if err != nil {
				return zero, err
			}
			a, err = decodeAggregate(lifecycle)
			if err != nil || evidenceHash(a) != rec.AfterLifecycle {
				return zero, ErrConflict
			}
		}
		if evidenceHash(DurableRun(run)) != evidenceHash(rec.OriginalRun) && evidenceHash(DurableRun(run)) != evidenceHash(rec.RunAfter) {
			return zero, ErrConflict
		}
		if evidenceHash(original) != rec.BeforeCommand && evidenceHash(original) != evidenceHash(rec.CommandAfter) {
			return zero, ErrConflict
		}
		return FenceIntentTransition{Authority: authority, Lifecycle: lifecycle, Run: rec.RunAfter, Command: rec.CommandAfter}, nil
	}
	c := original.Command
	g := f.Request.Grant
	if original.Retired || c.ID != g.CommandID || c.Type != "run_assigned" || c.AgentID != g.AgentID || c.ActionID != run.ID || c.TopologyUID != g.Scope.TopologyUID || c.Run == nil || !SameRunIncarnation(*c.Run, run) || uint64(c.Attempt) != g.Attempt || (run.Status != controlplane.RunRunning && run.Status != controlplane.RunFailed) || run.HostCompletion != nil || run.Cleanup != nil {
		return zero, ErrConflict
	}
	next, err := PrepareIntentDispositionLifecycle(authority, lifecycle, d, run, observation, key, now)
	if err != nil {
		return zero, err
	}
	after, err := decodeAggregate(next)
	if err != nil {
		return zero, err
	}
	ended := DurableRun(run)
	ended.Status = controlplane.RunFailed
	ended.Recoverable = false
	ended.EndedAt = now
	ended.LeaseUntil = time.Time{}
	ended.Err = "execution fenced; original result unknown; explicit intent disposition " + d.ID
	retired := original
	retired.Retired = true
	retired.Command.Status = controlplane.AgentCommandStatusFailed
	retired.Command.EndedAt = now
	retired.Command.Err = ended.Err
	rec := &FenceIntentReconciliation{Decision: d, BeforeLifecycle: evidenceHash(a), AfterLifecycle: evidenceHash(after), BeforeCommand: evidenceHash(original), OriginalRun: DurableRun(run), RunAfter: ended, CommandAfter: retired, Observation: observation, HostKey: append([]byte(nil), key...), PreparedAt: now}
	f.IntentDisposition = rec
	auth.Fences[d.FenceID] = f
	auth.Version = intentDispositionAuthorityVersion
	auth.ObservedAt = now
	prepared, err := json.Marshal(auth)
	if err != nil {
		return zero, err
	}
	if _, err = decodeExecutionAuthority(prepared); err != nil {
		return zero, err
	}
	return FenceIntentTransition{Authority: prepared, Lifecycle: next, Run: ended, Command: retired}, nil
}

func FinishFenceIntent(authority, lifecycle []byte, run controlplane.Run, command CommandRecord, d controlplane.IntentDispositionDecision) ([]byte, error) {
	auth, err := decodeExecutionAuthority(authority)
	if err != nil {
		return nil, err
	}
	f, ok := auth.Fences[d.FenceID]
	if !ok || f.IntentDisposition == nil {
		return nil, ErrConflict
	}
	rec := f.IntentDisposition
	if evidenceHash(rec.Decision) != evidenceHash(d) {
		return nil, ErrConflict
	}
	a, err := decodeAggregate(lifecycle)
	if err != nil {
		return nil, err
	}
	if evidenceHash(a) != rec.AfterLifecycle || evidenceHash(DurableRun(run)) != evidenceHash(rec.RunAfter) || evidenceHash(command) != evidenceHash(rec.CommandAfter) {
		return nil, ErrConflict
	}
	if !rec.ReleasedAt.IsZero() {
		return authority, nil
	}
	g, ok := auth.Grants[grantScopeKey(f.Request.Grant.Scope)]
	if !ok || !sameGrantEpoch(g, f.Request.Grant) || g.State != controlplane.GrantRevoked {
		return nil, ErrConflict
	}
	for _, other := range auth.Grants {
		if (other.AgentID == g.AgentID || other.Scope.TopologyUID == g.Scope.TopologyUID) && other.State != controlplane.GrantStopped && !sameGrantEpoch(other, g) {
			return nil, ErrConflict
		}
	}
	hash, err := d.Digest()
	if err != nil {
		return nil, err
	}
	g.State = controlplane.GrantStopped
	g.StopReceipt = "intent-fenced:" + hash
	auth.Grants[grantScopeKey(g.Scope)] = g
	if auth.Stops == nil {
		auth.Stops = map[uint64]stoppedExecutionGrant{}
	}
	auth.Stops[g.Epoch] = stoppedExecutionGrant{Grant: g, Receipt: g.StopReceipt}
	rec.ReleasedAt = auth.ObservedAt
	f.IntentDisposition = rec
	auth.Fences[d.FenceID] = f
	barrier := auth.Maintenance[f.BarrierID]
	barrier.Released = true
	auth.Maintenance[f.BarrierID] = barrier
	next, err := json.Marshal(auth)
	if err != nil {
		return nil, err
	}
	if _, err = decodeExecutionAuthority(next); err != nil {
		return nil, err
	}
	return next, nil
}
