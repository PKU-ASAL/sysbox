package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResetRunRecoversExactSignedCompletionWithoutReplay(t *testing.T) {
	c, req, grant := runFixture(t, "reset")
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.HostKey = key
	var receipt controlplane.SignedExecutionStopReceipt
	c.ReportRunID = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		receipt = r
		return errors.New("API unavailable")
	}
	result := json.RawMessage(`{"id":"run","op":"reset","status":"failed","error":"reset interrupted"}`)
	release := make(chan struct{})
	_, err = c.StartAsync(context.Background(), context.Background(), req, grant, func(context.Context) (json.RawMessage, error) { <-release; return result, nil })
	require.NoError(t, err)
	active := c.findActive(grant)
	require.NotNil(t, active)
	close(release)
	<-active.done
	require.Equal(t, "run_reset", receipt.Grant.Operation)
	require.NoError(t, controlplane.VerifyRunStopReceipt(pub, receipt, result))
	require.Error(t, controlplane.VerifyRunStopReceipt(pub, receipt, json.RawMessage(`{"id":"other"}`)))
	reports := 0
	cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostBootID: "replacement", AgentID: c.AgentID, HostKey: key}
	cold.ReportRunID = func(_ context.Context, id string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		reports++
		require.Equal(t, req.RunID, id)
		require.Equal(t, receipt, r)
		require.JSONEq(t, string(result), string(raw))
		return nil
	}
	require.NoError(t, cold.Recover(context.Background()))
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
}
