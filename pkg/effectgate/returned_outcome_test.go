package effectgate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReturnedOutcomeSurvivesInterruptedLedgerPublication(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "host", SessionID: "session", ActionID: "adopt", CommandID: "cmd", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: "digest", TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	require.NoError(t, err)
	g := Gate{Root: t.TempDir(), PublicKey: pub, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	ledger := filepath.Join(g.Root, fmt.Sprintf("%x.json", sha256.Sum256([]byte("owner"))))
	_, err = g.ExecuteWithReturnProof(context.Background(), signed, "external", func(context.Context) (json.RawMessage, error) {
		require.NoError(t, os.Rename(ledger, ledger+".intent"))
		require.NoError(t, os.Mkdir(ledger, 0700))
		return json.RawMessage(`{"identity":"owned"}`), nil
	})
	require.Error(t, err)
	require.NoError(t, os.Remove(ledger))
	require.NoError(t, os.Rename(ledger+".intent", ledger))
	_, err = g.ReadOutcome(context.Background(), grant, "external")
	require.ErrorIs(t, err, ErrUncertain)
	for _, field := range []string{"epoch", "boot", "scope"} {
		wrong := grant
		switch field {
		case "epoch":
			wrong.Epoch++
		case "boot":
			wrong.TargetBootID = "other"
		case "scope":
			wrong.Scope.ResourceUID = "other"
		}
		_, err := g.RecoverReturnedOutcome(context.Background(), wrong, "external")
		require.Error(t, err, field)
	}
	result, err := g.RecoverReturnedOutcome(context.Background(), grant, "external")
	require.NoError(t, err)
	require.JSONEq(t, `{"identity":"owned"}`, string(result))
	_, err = g.Execute(context.Background(), signed, "external", func(context.Context) (json.RawMessage, error) {
		t.Fatal("returned provider callback replayed")
		return nil, nil
	})
	require.NoError(t, err)
	other := grant
	other.Digest = "other"
	_, err = g.RecoverReturnedOutcome(context.Background(), other, "external")
	require.Error(t, err)
}

func TestErroredOrCancelledAdoptionNeverCreatesReturnProof(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "host", SessionID: "session", ActionID: "adopt", CommandID: "cmd", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: "digest", TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
			signed, err := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
			require.NoError(t, err)
			g := Gate{Root: t.TempDir(), PublicKey: pub, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err = g.ExecuteWithReturnProof(ctx, signed, "external", func(context.Context) (json.RawMessage, error) {
				if mode == "cancel" {
					cancel()
					return json.RawMessage(`{"result":"owned"}`), nil
				}
				return nil, fmt.Errorf("lost response")
			})
			require.ErrorIs(t, err, ErrUncertain)
			require.NoFileExists(t, g.returnedPath(grant, "external"))
			_, err = g.RecoverReturnedOutcome(context.Background(), grant, "external")
			require.ErrorIs(t, err, ErrUncertain)
		})
	}
}

func TestReturnedProofCorruptionCannotCompleteIntent(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "host", SessionID: "session", ActionID: "adopt", CommandID: "cmd", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: "digest", TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: time.Now().UTC().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, grant, "boot", time.Now())
	require.NoError(t, err)
	g := Gate{Root: t.TempDir(), PublicKey: pub, BootID: "boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
	_, err = g.ExecuteWithReturnProof(context.Background(), signed, "external", func(context.Context) (json.RawMessage, error) { return nil, fmt.Errorf("unknown") })
	require.Error(t, err)
	require.NoError(t, g.RecoverReturned(context.Background()))
	bad := signed
	bad.Signature = nil
	raw, err := json.Marshal(returnedOutcome{Grant: bad, EffectID: "external", Result: json.RawMessage(`{"owned":true}`)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(g.returnedPath(grant, "external"), raw, 0600))
	require.Error(t, g.RecoverReturned(context.Background()))
	_, err = g.ReadOutcome(context.Background(), grant, "external")
	require.ErrorIs(t, err, ErrUncertain)
	other := signed
	other.Grant.Operation = "gc_delete"
	_, err = g.ExecuteWithReturnProof(context.Background(), other, "external", func(context.Context) (json.RawMessage, error) { t.Fatal("non-adoption executed"); return nil, nil })
	require.ErrorIs(t, err, ErrFenced)
}
