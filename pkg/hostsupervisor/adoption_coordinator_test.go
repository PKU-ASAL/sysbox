package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func adoptionFixture(t *testing.T) (*AdoptionCoordinator, AdoptionRequest, controlplane.SignedExecutionGrant) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	req := AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: "topology", Address: "sysbox_node.web", Provider: "docker", AgentID: "agent", ExternalID: "external", ResourceUID: "resource", Generation: 1, ExpectedSerial: 4, Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "sha256:evidence"}}
	d, e := req.Digest()
	require.NoError(t, e)
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}, AgentID: "agent", SessionID: "session", ActionID: "adopt", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: d, TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
	signed, e := controlplane.SignExecutionGrant(key, g, "host", time.Now())
	require.NoError(t, e)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	_, host, e := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, e)
	return &AdoptionCoordinator{Root: root, IssuerKey: pub, HostKey: host, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}, HostBootID: "host", AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}, req, signed
}
func TestAdoptionApplyLostReplyObservesWithoutReapplying(t *testing.T) {
	c, req, g := adoptionFixture(t)
	calls := 0
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { calls++; return nil, errors.New("reply lost") })
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
	_, err = c.Observe(context.Background(), req, g)
	require.ErrorIs(t, err, effectgate.ErrUncertain)
	require.Equal(t, 1, calls)
}
func TestAdoptionRejectsClientEvidenceMismatch(t *testing.T) {
	c, req, g := adoptionFixture(t)
	req.Evidence.Fingerprint = "forged"
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	require.Error(t, err)
	_ = c
}

func TestAdoptionCompletionReceiptSurvivesLostAPIReply(t *testing.T) {
	c, req, g := adoptionFixture(t)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.HostKey = host
	var receipts []controlplane.SignedExecutionStopReceipt
	c.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		require.NoError(t, controlplane.VerifyAdoptionStopReceipt(host.Public().(ed25519.PublicKey), r, raw))
		receipts = append(receipts, r)
		return errors.New("API reply lost")
	}
	calls := 0
	_, err = c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"container_name":"owned"}`), nil
	})
	require.ErrorContains(t, err, "API reply lost")
	cold := *c
	cold.HostBootID = "new-boot"
	cold.Authorize = nil
	cold.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		receipts = append(receipts, r)
		return nil
	}
	raw, err := cold.Observe(context.Background(), req, g)
	require.NoError(t, err)
	require.JSONEq(t, `{"container_name":"owned"}`, string(raw))
	require.Len(t, receipts, 2)
	require.Equal(t, receipts[0], receipts[1])
	require.Equal(t, 1, calls)
}

func TestAdoptionRejectsMissingReporterBeforeEffects(t *testing.T) {
	c, req, g := adoptionFixture(t)
	c.Report = nil
	called := false
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { called = true; return json.RawMessage(`{}`), nil })
	require.ErrorIs(t, err, effectgate.ErrFenced)
	require.False(t, called)
}

func TestAdoptionHistoricalObserveRejectsForgedSignature(t *testing.T) {
	c, req, g := adoptionFixture(t)
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"owned":true}`), nil })
	require.NoError(t, err)
	g.Signature = make([]byte, ed25519.SignatureSize)
	_, err = c.Observe(context.Background(), req, g)
	require.Error(t, err)
}

func TestAdoptionRecoveryReportsWithoutWorkerObserve(t *testing.T) {
	c, req, g := adoptionFixture(t)
	var first controlplane.SignedExecutionStopReceipt
	c.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		first = r
		return errors.New("lost acknowledgment")
	}
	_, err := c.Apply(context.Background(), req, g, func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"owned":true}`), nil })
	require.Error(t, err)
	cold := *c
	cold.Authorize = nil
	cold.HostBootID = "restarted"
	reports := 0
	cold.Report = func(_ context.Context, id string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
		reports++
		require.Equal(t, req.ID, id)
		require.Equal(t, first, r)
		require.JSONEq(t, `{"owned":true}`, string(raw))
		return nil
	}
	require.NoError(t, cold.Recover(context.Background()))
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
}

func TestAdoptionRecoverySealsCompletedOutcomeButNotUnknownIntent(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "complete"}[complete], func(t *testing.T) {
			c, req, g := adoptionFixture(t)
			gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
			_, err := gate.Execute(context.Background(), g, req.ExternalID, func(context.Context) (json.RawMessage, error) {
				if !complete {
					return nil, errors.New("provider outcome unknown")
				}
				return json.RawMessage(`{"owned":true}`), nil
			})
			if complete {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			reports := 0
			c.HostBootID = "new-boot"
			c.Authorize = nil
			c.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, raw json.RawMessage) error {
				reports++
				require.NoError(t, controlplane.VerifyAdoptionStopReceipt(c.HostKey.Public().(ed25519.PublicKey), r, raw))
				return nil
			}
			require.NoError(t, c.Recover(context.Background()))
			if complete {
				require.Equal(t, 1, reports)
			} else {
				require.Zero(t, reports)
			}
		})
	}
}
