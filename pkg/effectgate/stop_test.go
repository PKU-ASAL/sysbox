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

func fixedGateFixture(t *testing.T) (*Gate, controlplane.SignedExecutionGrant, func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error)) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 2}, AgentID: "agent", SessionID: "session", ActionID: "pause-action", CommandID: "command", Epoch: 3, Attempt: 1, Operation: "node_pause", Digest: "request", ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	signed, err := controlplane.SignExecutionGrant(key, grant, "host-boot", time.Now())
	require.NoError(t, err)
	gate := &Gate{Root: t.TempDir(), PublicKey: pub, BootID: "host-boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	sign := func(g controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignExecutionStopReceipt(host, g, "host-boot", rand.Text(), time.Now())
	}
	return gate, signed, sign
}

func TestSealStopPersistsReceiptAndReadOutcomeAfterRetirement(t *testing.T) {
	gate, grant, sign := fixedGateFixture(t)
	ctx := context.Background()
	effects := 0
	_, err := gate.Execute(ctx, grant, "power", func(context.Context) (json.RawMessage, error) {
		effects++
		return json.RawMessage(`{"paused":true}`), nil
	})
	require.NoError(t, err)
	first, err := gate.SealStop(ctx, grant.Grant, "power", sign)
	require.NoError(t, err)
	cold := *gate
	again, err := cold.SealStop(ctx, grant.Grant, "power", func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		t.Fatal("must reuse durable receipt")
		return controlplane.SignedExecutionStopReceipt{}, nil
	})
	require.NoError(t, err)
	require.Equal(t, first, again)
	result, err := cold.ReadOutcome(ctx, grant.Grant, "power")
	require.NoError(t, err)
	require.JSONEq(t, `{"paused":true}`, string(result))
	_, err = cold.Execute(ctx, grant, "new-effect", func(context.Context) (json.RawMessage, error) { effects++; return nil, nil })
	require.Error(t, err)
	require.Equal(t, 1, effects)
	wrong := grant.Grant
	wrong.SessionID = "other"
	_, err = cold.ReadOutcome(ctx, wrong, "power")
	require.ErrorIs(t, err, ErrFenced)
}

func TestRecoverStopLostAcknowledgementNeverResignsOrReplays(t *testing.T) {
	gate, grant, sign := fixedGateFixture(t)
	ctx := context.Background()
	_, err := gate.Execute(ctx, grant, "power", func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.NoError(t, err)
	var receipts []controlplane.SignedExecutionStopReceipt
	report := func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return errors.New("ack lost")
	}
	require.Error(t, gate.RecoverStops(ctx, sign, report))
	require.Len(t, receipts, 1)
	cold := *gate
	require.NoError(t, cold.RecoverStops(ctx, func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		t.Fatal("must not resign")
		return controlplane.SignedExecutionStopReceipt{}, nil
	}, func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipts = append(receipts, r)
		return nil
	}))
	require.Equal(t, receipts[0], receipts[1])
	require.NoError(t, cold.RecoverStops(ctx, sign, func(context.Context, controlplane.SignedExecutionStopReceipt) error {
		t.Fatal("reported receipt must skip callback")
		return nil
	}))
}

func TestUnknownEffectNeverGetsStopReceipt(t *testing.T) {
	gate, grant, sign := fixedGateFixture(t)
	ctx := context.Background()
	_, err := gate.Execute(ctx, grant, "power", func(context.Context) (json.RawMessage, error) { return nil, errors.New("effect response lost") })
	require.ErrorIs(t, err, ErrUncertain)
	_, err = gate.SealStop(ctx, grant.Grant, "power", sign)
	require.ErrorIs(t, err, ErrUncertain)
	_, err = gate.ReadOutcome(ctx, grant.Grant, "power")
	require.ErrorIs(t, err, ErrUncertain)
	require.NoError(t, gate.RecoverStops(ctx, func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		t.Fatal("intent cannot be sealed")
		return controlplane.SignedExecutionStopReceipt{}, nil
	}, func(context.Context, controlplane.SignedExecutionStopReceipt) error {
		t.Fatal("intent cannot report stop")
		return nil
	}))
}

func TestSealStopRejectsDifferentGrantAndSigningFailureWithoutRetiring(t *testing.T) {
	gate, signed, sign := fixedGateFixture(t)
	ctx := context.Background()
	_, err := gate.Execute(ctx, signed, "power", func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.NoError(t, err)
	for _, mutate := range []func(*controlplane.ExecutionGrant){
		func(g *controlplane.ExecutionGrant) { g.AgentID = "other" }, func(g *controlplane.ExecutionGrant) { g.Scope.Generation++ }, func(g *controlplane.ExecutionGrant) { g.Attempt++ }, func(g *controlplane.ExecutionGrant) { g.ExpiresAt = g.ExpiresAt.Add(time.Second) },
	} {
		wrong := signed.Grant
		mutate(&wrong)
		_, err := gate.SealStop(ctx, wrong, "power", sign)
		require.ErrorIs(t, err, ErrFenced)
	}
	_, err = gate.SealStop(ctx, signed.Grant, "power", func(controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignedExecutionStopReceipt{}, errors.New("signer unavailable")
	})
	require.Error(t, err)
	replayed, err := gate.Execute(ctx, signed, "power", func(context.Context) (json.RawMessage, error) { t.Fatal("must not replay effect"); return nil, nil })
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(replayed))
	_, err = gate.SealStop(ctx, signed.Grant, "power", sign)
	require.NoError(t, err)
}

func TestConcurrentSealStopSignsOnce(t *testing.T) {
	gate, signed, sign := fixedGateFixture(t)
	ctx := context.Background()
	_, err := gate.Execute(ctx, signed, "power", func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.NoError(t, err)
	var calls atomic.Int32
	var wg sync.WaitGroup
	receipts := make(chan controlplane.SignedExecutionStopReceipt, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := *gate
			r, err := copy.SealStop(ctx, signed.Grant, "power", func(g controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
				calls.Add(1)
				return sign(g)
			})
			receipts <- r
			failures <- err
		}()
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var first *controlplane.SignedExecutionStopReceipt
	for receipt := range receipts {
		if first == nil {
			copy := receipt
			first = &copy
		} else {
			require.Equal(t, *first, receipt)
		}
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestGrantWithTwoCompletedEffectsUsesOneStopReceipt(t *testing.T) {
	gate, signed, sign := fixedGateFixture(t)
	ctx := context.Background()
	for _, id := range []string{"first", "second"} {
		_, err := gate.Execute(ctx, signed, id, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
		require.NoError(t, err)
	}
	first, err := gate.SealStop(ctx, signed.Grant, "first", sign)
	require.NoError(t, err)
	second, err := gate.SealStop(ctx, signed.Grant, "second", sign)
	require.NoError(t, err)
	require.Equal(t, first, second, "one grant cannot report contradictory stop receipt identities")
}
