package effectgate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type RunState string

const (
	RunIntent   RunState = "intent"
	RunComplete RunState = "complete"
)

// RunReservation is durable admission evidence, not proof of a live callback.
// Only Admitted permits starting an effect. An existing intent without an
// entry in the supervisor's active registry remains uncertain after restart.
type RunReservation struct {
	Admitted   bool
	State      RunState
	Grant      controlplane.SignedExecutionGrant
	Result     json.RawMessage
	Checkpoint json.RawMessage
}

func isRunGrant(g controlplane.ExecutionGrant) bool {
	return g.Operation == "run_apply" || g.Operation == "run_destroy"
}

func sameRunIdentity(a, b controlplane.ExecutionGrant) bool {
	a.ExpiresAt, b.ExpiresAt = time.Time{}, time.Time{}
	return a == b
}

func sameRunEnvelope(a, b controlplane.SignedExecutionGrant) bool {
	return sameGrant(a.Grant, b.Grant) && a.BootID == b.BootID && a.IssuedAt.Equal(b.IssuedAt) && bytes.Equal(a.Signature, b.Signature)
}

func cloneRunEnvelope(s controlplane.SignedExecutionGrant) controlplane.SignedExecutionGrant {
	s.Signature = append([]byte(nil), s.Signature...)
	return s
}

func (g *Gate) verifyRun(s controlplane.SignedExecutionGrant, effectID string) error {
	if effectID == "" || g.Authorize == nil || !isRunGrant(s.Grant) {
		return ErrFenced
	}
	return controlplane.VerifyExecutionGrant(g.PublicKey, s, g.BootID, time.Now())
}

func (g *Gate) authorizeRun(ctx context.Context, s controlplane.SignedExecutionGrant, effectID string) error {
	if err := g.verifyRun(s, effectID); err != nil {
		return err
	}
	if err := g.Authorize(ctx, s.Grant); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Time can elapse while awaiting the authority. Never commit a proof which
	// became expired during that request or while waiting for the journal lock.
	return g.verifyRun(s, effectID)
}

// ReadRun returns historical evidence for the exact Run identity, including its
// latest renewed envelope. Both signatures are checked at issuance against their
// historical boot; expiry, retirement and online authority do not govern reads.
// The returned reservation never authorizes starting a callback.
func (g *Gate) ReadRun(ctx context.Context, proof controlplane.SignedExecutionGrant, effectID string) (RunReservation, error) {
	var zero RunReservation
	if effectID == "" || !isRunGrant(proof.Grant) {
		return zero, ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(g.PublicKey, proof, proof.BootID, proof.IssuedAt); err != nil {
		return zero, err
	}
	_, record, release, err := g.locked(ctx, proof.Grant.Scope.TopologyUID)
	if err != nil {
		return zero, err
	}
	defer release()
	prior, ok := record.Outcomes[outcomeKey(proof.Grant, effectID)]
	if !ok {
		return zero, ErrNotFound
	}
	if prior.SignedGrant == nil || !sameRunIdentity(prior.Grant, proof.Grant) || prior.SignedGrant.BootID != proof.BootID {
		return zero, ErrFenced
	}
	return g.runReservation(prior)
}

// ReadRunEnvelope reads the exact durable grant for trusted recovery callers.
// The persisted signed envelope supplies the historical issuer proof; an
// unsigned legacy outcome cannot be upgraded into Run completion evidence.
func (g *Gate) ReadRunEnvelope(ctx context.Context, grant controlplane.ExecutionGrant, effectID string) (RunReservation, error) {
	var zero RunReservation
	if effectID == "" || !isRunGrant(grant) {
		return zero, ErrFenced
	}
	_, record, release, err := g.locked(ctx, grant.Scope.TopologyUID)
	if err != nil {
		return zero, err
	}
	defer release()
	prior, ok := record.Outcomes[outcomeKey(grant, effectID)]
	if !ok {
		return zero, ErrNotFound
	}
	if !sameGrant(prior.Grant, grant) {
		return zero, ErrFenced
	}
	return g.runReservation(prior)
}

func (g *Gate) runReservation(prior outcome) (RunReservation, error) {
	if prior.SignedGrant == nil {
		return RunReservation{}, ErrFenced
	}
	current := *prior.SignedGrant
	if err := controlplane.VerifyExecutionGrant(g.PublicKey, current, current.BootID, current.IssuedAt); err != nil {
		return RunReservation{}, err
	}
	return RunReservation{State: RunState(prior.State), Grant: cloneRunEnvelope(current), Result: append(json.RawMessage(nil), prior.Result...), Checkpoint: append(json.RawMessage(nil), prior.Checkpoint...)}, nil
}

// ReserveRun journals an intent under a short topology lock. It never executes
// a provider callback. The intent blocks all existing Gate.Execute admissions
// until CommitRun establishes a complete outcome; expiry does not release it.
func (g *Gate) ReserveRun(ctx context.Context, signed controlplane.SignedExecutionGrant, effectID string) (RunReservation, error) {
	var zero RunReservation
	if err := g.verifyRun(signed, effectID); err != nil {
		return zero, err
	}
	path, record, release, err := g.locked(ctx, signed.Grant.Scope.TopologyUID)
	if err != nil {
		return zero, err
	}
	defer release()
	grant := signed.Grant
	if grant.Epoch < record.epochFloor(grant.Scope.ResourceUID) || record.Stopped[grant.Epoch] {
		return zero, ErrFenced
	}
	key := outcomeKey(grant, effectID)
	if prior, ok := record.Outcomes[key]; ok {
		if prior.SignedGrant == nil || !sameRunIdentity(prior.Grant, grant) {
			return zero, ErrFenced
		}
		if err := g.authorizeRun(ctx, *prior.SignedGrant, effectID); err != nil {
			return zero, err
		}
		return RunReservation{State: RunState(prior.State), Grant: cloneRunEnvelope(*prior.SignedGrant), Result: append(json.RawMessage(nil), prior.Result...)}, nil
	}
	for _, prior := range record.Outcomes {
		if prior.State != "complete" {
			return zero, ErrUncertain
		}
	}
	if err := g.authorizeRun(ctx, signed, effectID); err != nil {
		return zero, err
	}
	copy := cloneRunEnvelope(signed)
	record.Epochs[grant.Scope.ResourceUID] = grant.Epoch
	record.Outcomes[key] = outcome{Grant: grant, SignedGrant: &copy, State: string(RunIntent)}
	if err := save(path, record); err != nil {
		return zero, err
	}
	return RunReservation{Admitted: true, State: RunIntent, Grant: cloneRunEnvelope(signed)}, nil
}

// RenewRun replaces exactly the current signed envelope. A duplicate or stale
// renewal is rejected rather than being treated as another deadline extension.
// The caller must also serialize this operation with its monotonic watchdog:
// journal renewal cannot revive a canceled callback or authorize cold replay.
func (g *Gate) RenewRun(ctx context.Context, old, renewed controlplane.SignedExecutionGrant, effectID string) error {
	if err := g.verifyRun(old, effectID); err != nil {
		return err
	}
	if err := g.verifyRun(renewed, effectID); err != nil {
		return err
	}
	if !sameRunIdentity(old.Grant, renewed.Grant) || old.BootID != renewed.BootID || !renewed.Grant.ExpiresAt.After(old.Grant.ExpiresAt) || renewed.IssuedAt.Before(old.IssuedAt) {
		return ErrFenced
	}
	path, record, release, err := g.locked(ctx, old.Grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	defer release()
	prior, err := currentRunIntent(record, old, effectID)
	if err != nil {
		return err
	}
	if err := g.authorizeRun(ctx, renewed, effectID); err != nil {
		return err
	}
	if err := g.verifyRun(old, effectID); err != nil {
		return err
	}
	copy := cloneRunEnvelope(renewed)
	prior.Grant, prior.SignedGrant = renewed.Grant, &copy
	record.Outcomes[outcomeKey(old.Grant, effectID)] = prior
	return save(path, record)
}

func currentRunIntent(record *ledger, signed controlplane.SignedExecutionGrant, effectID string) (outcome, error) {
	prior, ok := record.Outcomes[outcomeKey(signed.Grant, effectID)]
	if !ok {
		return prior, ErrNotFound
	}
	if prior.State != string(RunIntent) || prior.SignedGrant == nil || !sameRunEnvelope(*prior.SignedGrant, signed) || record.Stopped[signed.Grant.Epoch] || signed.Grant.Epoch < record.epochFloor(signed.Grant.Scope.ResourceUID) {
		return prior, ErrFenced
	}
	return prior, nil
}

// CommitRun stores the callback's terminal result only against its exact latest
// envelope. Callers must establish actual callback/child quiescence first and
// serialize against renewal and cancellation. No error or timeout is stop proof.
func (g *Gate) CommitRun(ctx context.Context, current controlplane.SignedExecutionGrant, effectID string, result json.RawMessage) error {
	if err := g.verifyRun(current, effectID); err != nil {
		return err
	}
	if len(result) == 0 {
		result = json.RawMessage(`null`)
	}
	if !json.Valid(result) {
		return ErrUncertain
	}
	path, record, release, err := g.locked(ctx, current.Grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	defer release()
	prior, err := currentRunIntent(record, current, effectID)
	if err != nil {
		return err
	}
	if err := g.authorizeRun(ctx, current, effectID); err != nil {
		return err
	}
	prior.State, prior.Result = string(RunComplete), append(json.RawMessage(nil), result...)
	record.Outcomes[outcomeKey(current.Grant, effectID)] = prior
	return save(path, record)
}

// CommitQuiescentRun records a failed or cancelled outcome after authority has
// closed. It authorizes no provider effect and therefore verifies historical
// signatures rather than requiring an unexpired grant or online authorization.
// This is a trusted host-only primitive: its caller MUST establish actual
// callback quiescence, close and drain child admission, and verify all owned
// children stopped. Canonical parent-stop preparation must also check the
// authoritative child inventory before reporting the outcome. Missing active
// callbacks, expiry, cancellation and legacy intents are not quiescence proof.
// Never expose this method as an RPC or use it to infer an old callback exited.
// Exact completed replays only confirm existing evidence, including after a
// successor starts; they never change the result, receipt or execution authority.
func (g *Gate) CommitQuiescentRun(ctx context.Context, current controlplane.SignedExecutionGrant, effectID string, result json.RawMessage) error {
	if effectID == "" || !isRunGrant(current.Grant) {
		return ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(g.PublicKey, current, current.BootID, current.IssuedAt); err != nil {
		return err
	}
	var run controlplane.Run
	if err := json.Unmarshal(result, &run); err != nil || run.ID == "" || run.AgentID != current.Grant.AgentID || run.EndedAt.IsZero() ||
		(run.Status != controlplane.RunFailed && run.Status != controlplane.RunCancelled) || "run_"+run.Op != current.Grant.Operation {
		return ErrUncertain
	}
	path, record, release, err := g.locked(ctx, current.Grant.Scope.TopologyUID)
	if err != nil {
		return err
	}
	defer release()
	if prior, ok := record.Outcomes[outcomeKey(current.Grant, effectID)]; ok && prior.State == string(RunComplete) {
		if prior.SignedGrant == nil || !sameRunEnvelope(*prior.SignedGrant, current) {
			return ErrFenced
		}
		// Match save's RawMessage encoding (including HTML escaping), without
		// decoding into a map that could discard fields or lose number precision.
		stored, storedErr := json.Marshal(prior.Result)
		replay, replayErr := json.Marshal(result)
		if storedErr != nil || replayErr != nil || !bytes.Equal(stored, replay) {
			return ErrFenced
		}
		// A previous save may have renamed the file but failed directory sync.
		// Finish that durability barrier without rewriting historical evidence.
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	prior, err := currentRunIntent(record, current, effectID)
	if err != nil {
		return err
	}
	if _, err := g.runReservation(prior); err != nil {
		return err
	}
	prior.State, prior.Result = string(RunComplete), append(json.RawMessage(nil), result...)
	record.Outcomes[outcomeKey(current.Grant, effectID)] = prior
	return save(path, record)
}
