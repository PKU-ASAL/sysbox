package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func runFixture(t *testing.T) (*RunCoordinator, RunRequest, controlplane.SignedExecutionGrant) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := RunRequest{RunID: "run", Topology: "lab", TopologyUID: "uid", Revision: "rev", ActionID: "action", ResourceUID: "resource", Generation: 1, Action: "apply", Target: "sysbox_node.web", ExpectedSerial: 1}
	digest, err := req.Digest()
	require.NoError(t, err)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "uid", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: digest, State: controlplane.GrantActive, TargetBootID: "host", ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	return &RunCoordinator{Root: root, IssuerKey: pub, HostBootID: "host", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, signed
}

func TestRunCoordinatorRequiresSignatureAndOnlineAuthority(t *testing.T) {
	c, req, g := runFixture(t)
	effect := func(context.Context) (json.RawMessage, error) {
		t.Error("unauthorized callback invoked")
		return nil, nil
	}
	bad := g
	bad.Signature = make([]byte, ed25519.SignatureSize)
	_, err := c.StartAsync(context.Background(), context.Background(), req, bad, effect)
	require.Error(t, err)
	c.Authorize = nil
	_, err = c.StartAsync(context.Background(), context.Background(), req, g, effect)
	require.Error(t, err)
}

func TestRunCoordinatorPersistsIntentAndNeverReplaysLostEffect(t *testing.T) {
	c, req, g := runFixture(t)
	_, err := c.StartAsync(context.Background(), context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return nil, errors.New("reply lost") })
	require.NoError(t, err)
	require.Eventually(t, func() bool { return c.findActive(g) == nil }, time.Second, time.Millisecond)
	out, err := c.ObserveAsync(context.Background(), req, g)
	require.NoError(t, err)
	require.Equal(t, "uncertain", out.Status)
	out, err = c.StartAsync(context.Background(), context.Background(), req, g, func(context.Context) (json.RawMessage, error) { t.Error("intent replayed"); return nil, nil })
	require.NoError(t, err)
	require.Equal(t, "uncertain", out.Status)
}

func TestRunCoordinatorRecoveryRetriesExactReceiptAndResult(t *testing.T) {
	c, req, g := runFixture(t)
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.HostKey = hostKey
	var first controlplane.SignedExecutionStopReceipt
	c.ReportRunID = func(_ context.Context, id string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		first = r
		return errors.New("API unavailable")
	}
	result := json.RawMessage(`{"id":"run","status":"done"}`)
	release := make(chan struct{})
	_, err = c.StartAsync(context.Background(), context.Background(), req, g, func(context.Context) (json.RawMessage, error) { <-release; return result, nil })
	require.NoError(t, err)
	active := c.findActive(g)
	close(release)
	<-active.done
	require.NotEmpty(t, first.Signature)
	cold := &RunCoordinator{Root: c.Root, IssuerKey: c.IssuerKey, HostBootID: "replacement-host", AgentID: c.AgentID, HostKey: hostKey}
	reports := 0
	cold.ReportRunID = func(_ context.Context, id string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		reports++
		require.Equal(t, req.RunID, id)
		require.Equal(t, first, r)
		require.JSONEq(t, string(result), string(raw))
		require.NoError(t, controlplane.VerifyRunStopReceipt(hostPub, r, raw))
		return nil
	}
	require.NoError(t, cold.Recover(context.Background()))
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
	out, err := cold.ObserveAsync(context.Background(), req, g)
	require.NoError(t, err)
	require.Equal(t, "completed", out.Status)
	require.JSONEq(t, string(result), string(out.Evidence))
}

func TestRunObserveSealsCompletedOutcomeAfterCrashBeforeReceipt(t *testing.T) {
	c, req, g := runFixture(t)
	pub, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	raw := json.RawMessage(`{"id":"run","status":"done"}`)
	gate, err := c.gate(g)
	require.NoError(t, err)
	_, err = gate.ReserveRun(context.Background(), g, req.ActionID)
	require.NoError(t, err)
	require.NoError(t, gate.CommitRun(context.Background(), g, req.ActionID, raw))
	c.HostKey = host
	c.HostBootID = "new-host-boot"
	c.Authorize = nil
	called := false
	c.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		called = true
		require.Equal(t, req.RunID, id)
		require.NoError(t, controlplane.VerifyRunStopReceipt(pub, receipt, evidence))
		return nil
	}
	out, err := c.ObserveAsync(context.Background(), req, g)
	require.NoError(t, err)
	require.Equal(t, "completed", out.Status)
	require.JSONEq(t, string(raw), string(out.Evidence))
	require.True(t, called)
}
