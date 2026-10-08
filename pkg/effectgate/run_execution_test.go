package effectgate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func runGateFixture(t *testing.T) (*Gate, controlplane.SignedExecutionGrant, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	grant := controlplane.ExecutionGrant{TargetBootID: "boot", Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: "digest", ExpiresAt: now.Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(key, grant, "boot", now)
	require.NoError(t, err)
	return &Gate{Root: t.TempDir(), PublicKey: pub, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}, signed, key
}

func extendedRunGrant(t *testing.T, key ed25519.PrivateKey, old controlplane.SignedExecutionGrant, delta time.Duration) controlplane.SignedExecutionGrant {
	t.Helper()
	grant := old.Grant
	grant.ExpiresAt = grant.ExpiresAt.Add(delta)
	signed, err := controlplane.SignExecutionGrant(key, grant, old.BootID, time.Now().UTC())
	require.NoError(t, err)
	return signed
}

func TestReserveRunAdmitsOnlyOnceAndBlocksGenericEffects(t *testing.T) {
	gate, signed, key := runGateFixture(t)
	ctx := context.Background()
	var admitted atomic.Int32
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			cold := *gate
			reservation, err := cold.ReserveRun(ctx, signed, "effect")
			if err != nil {
				t.Error(err)
				return
			}
			if reservation.Admitted {
				admitted.Add(1)
			}
		}()
	}
	workers.Wait()
	require.EqualValues(t, 1, admitted.Load())
	next := signed.Grant
	next.Epoch++
	next.ActionID = "successor"
	proof, err := controlplane.SignExecutionGrant(key, next, "boot", time.Now())
	require.NoError(t, err)
	_, err = gate.Execute(ctx, proof, "successor", func(context.Context) (json.RawMessage, error) {
		t.Fatal("pending Run must block successor")
		return nil, nil
	})
	require.ErrorIs(t, err, ErrUncertain)
	_, err = gate.ReserveRun(ctx, proof, "successor")
	require.ErrorIs(t, err, ErrUncertain)
}

func TestRunRenewalCASAndExactCompletion(t *testing.T) {
	gate, old, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	renewed := extendedRunGrant(t, key, old, time.Minute)
	require.NoError(t, gate.RenewRun(ctx, old, renewed, "effect"))
	require.ErrorIs(t, gate.RenewRun(ctx, old, renewed, "effect"), ErrFenced)
	later := extendedRunGrant(t, key, renewed, time.Minute)
	require.ErrorIs(t, gate.RenewRun(ctx, old, later, "effect"), ErrFenced)
	require.ErrorIs(t, gate.RenewRun(ctx, renewed, old, "effect"), ErrFenced)
	require.ErrorIs(t, gate.CommitRun(ctx, old, "effect", json.RawMessage(`{"stale":true}`)), ErrFenced)
	cold := *gate
	reservation, err := cold.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	require.False(t, reservation.Admitted)
	require.Equal(t, RunIntent, reservation.State)
	require.Equal(t, renewed.Signature, reservation.Grant.Signature)
	require.NoError(t, cold.CommitRun(ctx, renewed, "effect", json.RawMessage(`{"done":true}`)))
	require.ErrorIs(t, cold.RenewRun(ctx, renewed, later, "effect"), ErrFenced)
	reservation, err = cold.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	require.False(t, reservation.Admitted)
	require.Equal(t, RunComplete, reservation.State)
	require.JSONEq(t, `{"done":true}`, string(reservation.Result))
	_, err = cold.ReadOutcome(ctx, old.Grant, "effect")
	require.ErrorIs(t, err, ErrFenced, "existing receipt/outcome API retains exact grant matching")
	_, err = cold.ReadOutcome(ctx, renewed.Grant, "effect")
	require.NoError(t, err)
}

func TestRunGateRequiresIssuerAndOnlineAuthority(t *testing.T) {
	for _, missing := range []string{"issuer", "authorize", "denied"} {
		t.Run(missing, func(t *testing.T) {
			gate, old, key := runGateFixture(t)
			ctx := context.Background()
			_, err := gate.ReserveRun(ctx, old, "effect")
			require.NoError(t, err)
			renewed := extendedRunGrant(t, key, old, time.Minute)
			switch missing {
			case "issuer":
				gate.PublicKey = nil
			case "authorize":
				gate.Authorize = nil
			case "denied":
				gate.Authorize = func(context.Context, controlplane.ExecutionGrant) error { return errors.New("revoked") }
			}
			_, err = gate.ReserveRun(ctx, old, "effect")
			require.Error(t, err)
			require.Error(t, gate.RenewRun(ctx, old, renewed, "effect"))
			require.Error(t, gate.CommitRun(ctx, old, "effect", json.RawMessage(`{}`)))
		})
	}
}

func TestRunRenewalRejectsChangedIdentityAndForgedEnvelope(t *testing.T) {
	gate, old, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	for _, field := range []string{"epoch", "digest", "attempt", "boot", "signature", "operation"} {
		t.Run(field, func(t *testing.T) {
			grant := old.Grant
			grant.ExpiresAt = grant.ExpiresAt.Add(time.Minute)
			switch field {
			case "epoch":
				grant.Epoch++
			case "digest":
				grant.Digest = "changed"
			case "attempt":
				grant.Attempt++
			case "boot":
				grant.TargetBootID = "other"
			case "operation":
				grant.Operation = "run_destroy"
			}
			boot := old.BootID
			if field == "boot" {
				boot = "other"
			}
			proof, err := controlplane.SignExecutionGrant(key, grant, boot, time.Now())
			require.NoError(t, err)
			if field == "signature" {
				proof.Signature[0] ^= 1
			}
			require.Error(t, gate.RenewRun(ctx, old, proof, "effect"))
		})
	}
}

func TestRunRenewalConcurrentCASHasOneWinner(t *testing.T) {
	gate, old, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	proofs := []controlplane.SignedExecutionGrant{extendedRunGrant(t, key, old, time.Minute), extendedRunGrant(t, key, old, 2*time.Minute)}
	var winners atomic.Int32
	var workers sync.WaitGroup
	for _, proof := range proofs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := gate.RenewRun(ctx, old, proof, "effect")
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrFenced) {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	require.EqualValues(t, 1, winners.Load())
}

func TestExpiredRunIntentCannotRenewCommitOrAdmitSuccessor(t *testing.T) {
	gate, original, key := runGateFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	grant := original.Grant
	grant.ExpiresAt = now.Add(controlplane.ExecutionGrantMaxClockSkew + 100*time.Millisecond)
	old, err := controlplane.SignExecutionGrant(key, grant, "boot", now)
	require.NoError(t, err)
	_, err = gate.ReserveRun(ctx, old, "effect")
	require.NoError(t, err)
	renewed := extendedRunGrant(t, key, old, time.Minute)
	time.Sleep(110 * time.Millisecond)
	require.Error(t, gate.RenewRun(ctx, old, renewed, "effect"))
	require.Error(t, gate.CommitRun(ctx, old, "effect", json.RawMessage(`{}`)))
	_, err = gate.ReserveRun(ctx, renewed, "effect")
	require.Error(t, err, "new signed envelope cannot resurrect expired durable intent")
	successor := renewed.Grant
	successor.Epoch++
	successor.ActionID = "successor"
	proof, err := controlplane.SignExecutionGrant(key, successor, "boot", time.Now())
	require.NoError(t, err)
	_, err = gate.ReserveRun(ctx, proof, "successor")
	require.ErrorIs(t, err, ErrUncertain)
}

func TestRunOnlineCheckCannotCarryAdmissionPastExpiry(t *testing.T) {
	gate, original, key := runGateFixture(t)
	now := time.Now().UTC()
	grant := original.Grant
	grant.ExpiresAt = now.Add(controlplane.ExecutionGrantMaxClockSkew + 30*time.Millisecond)
	proof, err := controlplane.SignExecutionGrant(key, grant, "boot", now)
	require.NoError(t, err)
	gate.Authorize = func(context.Context, controlplane.ExecutionGrant) error {
		time.Sleep(40 * time.Millisecond)
		return nil
	}
	_, err = gate.ReserveRun(context.Background(), proof, "effect")
	require.Error(t, err)
}

func TestRunReservationVerifiesColdStoredSignature(t *testing.T) {
	gate, proof, _ := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, proof, "effect")
	require.NoError(t, err)
	path, record, release, err := gate.locked(ctx, proof.Grant.Scope.TopologyUID)
	require.NoError(t, err)
	prior := record.Outcomes[outcomeKey(proof.Grant, "effect")]
	prior.SignedGrant.Signature[0] ^= 1
	record.Outcomes[outcomeKey(proof.Grant, "effect")] = prior
	require.NoError(t, save(path, record))
	release()
	cold := *gate
	_, err = cold.ReserveRun(ctx, proof, "effect")
	require.Error(t, err)
}

func TestReadRunReturnsLatestRenewedEnvelope(t *testing.T) {
	gate, proof, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, proof, "effect")
	require.NoError(t, err)
	renewed := extendedRunGrant(t, key, proof, time.Minute)
	require.NoError(t, gate.RenewRun(ctx, proof, renewed, "effect"))
	for _, state := range []RunState{RunIntent, RunComplete} {
		if state == RunComplete {
			require.NoError(t, gate.CommitRun(ctx, renewed, "effect", json.RawMessage(`{"done":true}`)))
		}
		cold := *gate
		cold.Authorize = nil
		got, err := cold.ReadRun(ctx, proof, "effect")
		require.NoError(t, err)
		require.False(t, got.Admitted)
		require.Equal(t, state, got.State)
		require.True(t, sameRunEnvelope(renewed, got.Grant))
		if state == RunComplete {
			require.JSONEq(t, `{"done":true}`, string(got.Result))
		}
	}
}

func TestReadRunHistoricalEvidenceSurvivesExpiryBootAndRetirement(t *testing.T) {
	for _, state := range []RunState{RunIntent, RunComplete} {
		t.Run(string(state), func(t *testing.T) {
			gate, original, key := runGateFixture(t)
			ctx := context.Background()
			issued := time.Now().UTC().Add(-time.Hour)
			grant := original.Grant
			grant.ExpiresAt = issued.Add(time.Minute)
			proof, err := controlplane.SignExecutionGrant(key, grant, original.BootID, issued)
			require.NoError(t, err)
			path, record, release, err := gate.locked(ctx, grant.Scope.TopologyUID)
			require.NoError(t, err)
			record.Outcomes[outcomeKey(grant, "effect")] = outcome{Grant: grant, SignedGrant: &proof, State: string(state), Result: json.RawMessage(`{"done":true}`)}
			record.Stopped[grant.Epoch] = true
			record.Epochs[grant.Scope.ResourceUID] = grant.Epoch + 1
			require.NoError(t, save(path, record))
			release()
			gate.BootID, gate.Authorize = "new-boot", nil
			got, err := gate.ReadRun(ctx, proof, "effect")
			require.NoError(t, err)
			require.False(t, got.Admitted)
			require.Equal(t, state, got.State)
			require.True(t, sameRunEnvelope(proof, got.Grant))
		})
	}
}

func TestReadRunRejectsForeignOrForgedProof(t *testing.T) {
	gate, original, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, original, "effect")
	require.NoError(t, err)
	for _, field := range []string{"action", "scope", "digest", "attempt", "epoch", "operation", "boot", "signature", "effect"} {
		t.Run(field, func(t *testing.T) {
			grant, boot, effect := original.Grant, original.BootID, "effect"
			switch field {
			case "action":
				grant.ActionID = "foreign"
			case "scope":
				grant.Scope.ResourceUID, grant.Scope.Generation = "foreign", 1
			case "digest":
				grant.Digest = "foreign"
			case "attempt":
				grant.Attempt++
			case "epoch":
				grant.Epoch++
			case "operation":
				grant.Operation = "run_destroy"
			case "boot":
				grant.TargetBootID, boot = "foreign", "foreign"
			case "effect":
				effect = "foreign"
			}
			proof, err := controlplane.SignExecutionGrant(key, grant, boot, time.Now())
			require.NoError(t, err)
			if field == "signature" {
				proof.Signature[0] ^= 1
			}
			_, err = gate.ReadRun(ctx, proof, effect)
			require.Error(t, err)
		})
	}
}

func TestReadRunRejectsUnverifiableStoredEnvelope(t *testing.T) {
	for _, damage := range []string{"missing", "signature"} {
		t.Run(damage, func(t *testing.T) {
			gate, proof, _ := runGateFixture(t)
			ctx := context.Background()
			_, err := gate.ReserveRun(ctx, proof, "effect")
			require.NoError(t, err)
			path, record, release, err := gate.locked(ctx, proof.Grant.Scope.TopologyUID)
			require.NoError(t, err)
			prior := record.Outcomes[outcomeKey(proof.Grant, "effect")]
			if damage == "missing" {
				prior.SignedGrant = nil
			} else {
				prior.SignedGrant.Signature[0] ^= 1
			}
			record.Outcomes[outcomeKey(proof.Grant, "effect")] = prior
			require.NoError(t, save(path, record))
			release()
			_, err = gate.ReadRun(ctx, proof, "effect")
			require.Error(t, err)
			_, err = gate.ReadRunEnvelope(ctx, proof.Grant, "effect")
			require.Error(t, err)
		})
	}
}

func TestReadRunEnvelopeRequiresExactDurableGrant(t *testing.T) {
	gate, proof, key := runGateFixture(t)
	ctx := context.Background()
	_, err := gate.ReserveRun(ctx, proof, "effect")
	require.NoError(t, err)
	renewed := extendedRunGrant(t, key, proof, time.Minute)
	require.NoError(t, gate.RenewRun(ctx, proof, renewed, "effect"))
	gate.Authorize, gate.BootID = nil, "replacement-boot"
	_, err = gate.ReadRunEnvelope(ctx, proof.Grant, "effect")
	require.ErrorIs(t, err, ErrFenced)
	got, err := gate.ReadRunEnvelope(ctx, renewed.Grant, "effect")
	require.NoError(t, err)
	require.False(t, got.Admitted)
	require.Equal(t, RunIntent, got.State)
	require.True(t, sameRunEnvelope(renewed, got.Grant))
}
