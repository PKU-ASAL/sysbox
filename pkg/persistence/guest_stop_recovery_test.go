package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func guestStopRecoveryFixture(t *testing.T) ([]byte, []byte, CommandRecord, controlplane.SignedExecutionStopReceipt) {
	t.Helper()
	raw, source, receipt := hostGuestFixture(t)
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	ctx := context.Background()
	session, err := r.OpenSession(ctx, "agent", "host", "")
	require.NoError(t, err)
	grant := receipt.Grant
	grant.SessionID = session.ID
	issued, err := r.Issue(ctx, grant, time.Minute)
	require.NoError(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	receipt, err = controlplane.SignExecutionTaskStopReceipt(key, *issued, "host", "guest", issued.ActionID, issued.Digest, *receipt.GuestResult, "receipt", time.Now())
	require.NoError(t, err)
	authority, err := r.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	return raw, authority, source, receipt
}

func TestGuestHostCompletionStagesResultAndStopTogether(t *testing.T) {
	raw, authority, source, receipt := guestStopRecoveryFixture(t)
	prepared, err := PrepareGuestExecutionHostCompletion("execution", raw, authority, func(string) (*CommandRecord, error) { return &source, nil }, "agent", receipt)
	require.NoError(t, err)
	require.Equal(t, "completed", prepared.Execution.Status)
	a, err := decodeExecutionAuthority(prepared.Authority)
	require.NoError(t, err)
	stopped := a.Grants[grantScopeKey(receipt.Grant.Scope)]
	require.Equal(t, controlplane.GrantStopped, stopped.State)
	require.NotEmpty(t, stopped.StopReceipt)

	// The file record committed, but authority publication did not. Redaction
	// can run before recovery; no host receipt or original payload remains.
	x := prepared.Execution
	x.Request = controlplane.GuestExecutionRequest{}
	x.Result.Stdout = ""
	redacted, err := encodeExecution(x)
	require.NoError(t, err)
	redacted, err = preserveGuestDispatch(prepared.Record, redacted)
	require.NoError(t, err)
	recovered, err := PrepareGuestExecutionStopRecovery("execution", redacted, authority, nil)
	require.NoError(t, err)
	require.Equal(t, redacted, recovered.Record)
	require.Equal(t, x.Version, recovered.Execution.Version)
	a, err = decodeExecutionAuthority(recovered.Authority)
	require.NoError(t, err)
	require.Equal(t, stopped, a.Grants[grantScopeKey(receipt.Grant.Scope)])

	// A stale scan after successful release must not affect a successor.
	prior := a.Grants[grantScopeKey(receipt.Grant.Scope)]
	successor := prior
	successor.ActionID = "next"
	successor.Epoch++
	successor.State, successor.StopReceipt = controlplane.GrantActive, ""
	a.Epoch = successor.Epoch
	a.Grants[grantScopeKey(successor.Scope)] = successor
	later, err := json.Marshal(a)
	require.NoError(t, err)
	replayed, err := PrepareGuestExecutionStopRecovery("execution", redacted, later, nil)
	require.NoError(t, err)
	a, err = decodeExecutionAuthority(replayed.Authority)
	require.NoError(t, err)
	require.Equal(t, successor, a.Grants[grantScopeKey(successor.Scope)])
}

func TestGuestStopRecoveryRejectsMissingOrMismatchedEvidence(t *testing.T) {
	raw, authority, source, receipt := guestStopRecoveryFixture(t)
	prepared, err := PrepareGuestExecutionHostCompletion("execution", raw, authority, func(string) (*CommandRecord, error) { return &source, nil }, "agent", receipt)
	require.NoError(t, err)
	for _, kind := range []string{"missing", "epoch", "hash", "command", "status"} {
		t.Run(kind, func(t *testing.T) {
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(prepared.Record, &object))
			var proof guestHostCompletionProof
			require.NoError(t, json.Unmarshal(object["host_completion"], &proof))
			switch kind {
			case "epoch":
				proof.Grant.Epoch++
			case "hash":
				proof.Receipt = "bad"
			case "command":
				proof.Grant.CommandID = "other"
			case "status":
				object["status"] = json.RawMessage(`"running"`)
			}
			object["host_completion"], err = json.Marshal(proof)
			require.NoError(t, err)
			if kind == "missing" {
				delete(object, "host_completion")
			}
			altered, err := json.Marshal(object)
			require.NoError(t, err)
			_, err = PrepareGuestExecutionStopRecovery("execution", altered, authority, nil)
			require.Error(t, err)
		})
	}
}
