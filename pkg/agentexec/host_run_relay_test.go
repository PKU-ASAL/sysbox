package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
)

func TestDecodeHostRunEvidenceRequiresPlainTerminalPayload(t *testing.T) {
	run := controlplane.Run{ID: "run-1", Status: controlplane.RunDone}
	raw, err := json.Marshal(struct {
		Result controlplane.Run `json:"result"`
	}{Result: run})
	require.NoError(t, err)
	got, err := decodeHostRunEvidence(raw)
	require.Error(t, err)

	raw, err = json.Marshal(run)
	require.NoError(t, err)
	got, err = decodeHostRunEvidence(raw)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)
}

type relayClient struct {
	start    func(context.Context) (hostsupervisor.RunObservation, error)
	observe  func(context.Context) (hostsupervisor.RunObservation, error)
	renew    func(controlplane.SignedExecutionGrant) error
	canceled int
	starts   int
}

func (c *relayClient) StartRun(ctx context.Context, _ hostsupervisor.RunRequest, _ controlplane.SignedExecutionGrant) (hostsupervisor.RunObservation, error) {
	c.starts++
	return c.start(ctx)
}
func (c *relayClient) ObserveRun(ctx context.Context, _ hostsupervisor.RunRequest, _ controlplane.SignedExecutionGrant) (hostsupervisor.RunObservation, error) {
	return c.observe(ctx)
}
func (c *relayClient) RenewRun(_ context.Context, _ hostsupervisor.RunRequest, g controlplane.SignedExecutionGrant) error {
	return c.renew(g)
}
func (c *relayClient) CancelRun(ctx context.Context, _ hostsupervisor.RunRequest, _ controlplane.SignedExecutionGrant) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.canceled++
	return nil
}

func relayGrant(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, controlplane.SignedExecutionGrant) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	g, err := controlplane.SignExecutionGrant(priv, controlplane.ExecutionGrant{TargetBootID: "boot", Scope: controlplane.ExecutionScope{TopologyUID: "top"}, AgentID: "agent", SessionID: "session", ActionID: "action", CommandID: "command", Attempt: 1, Epoch: 1, Operation: "run_apply", Digest: "digest", ExpiresAt: now.Add(controlplane.ExecutionGrantMaxClockSkew + 90*time.Millisecond), State: controlplane.GrantActive}, "boot", now)
	require.NoError(t, err)
	return pub, priv, g
}

func relayEvidence(t *testing.T, run controlplane.Run) hostsupervisor.RunObservation {
	t.Helper()
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	return hostsupervisor.RunObservation{Status: "completed", Evidence: raw}
}

func TestHostRelayLostStartReplyOnlyObservesOriginal(t *testing.T) {
	pub, _, grant := relayGrant(t)
	run := controlplane.Run{ID: "run", Topology: "top", Revision: "rev", Attempt: 3, LeaseOwner: "owner", Status: controlplane.RunDone}
	c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
		return hostsupervisor.RunObservation{}, errors.New("lost reply")
	}, observe: func(context.Context) (hostsupervisor.RunObservation, error) { return relayEvidence(t, run), nil }}
	got, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, run, grant, pub, nil)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)
	require.Equal(t, 1, c.starts)
	require.Zero(t, c.canceled)
}

func TestHostRelayRenewsAuthorityBeforeHost(t *testing.T) {
	pub, priv, grant := relayGrant(t)
	run := controlplane.Run{ID: "run", Status: controlplane.RunDone}
	apiRenewed, hostRenewed := false, false
	c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
		return hostsupervisor.RunObservation{Status: "running"}, nil
	}, observe: func(context.Context) (hostsupervisor.RunObservation, error) {
		require.True(t, hostRenewed)
		return relayEvidence(t, run), nil
	}, renew: func(next controlplane.SignedExecutionGrant) error {
		require.True(t, apiRenewed)
		hostRenewed = true
		return nil
	}}
	renew := func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
		apiRenewed = true
		g := grant.Grant
		g.ExpiresAt = time.Now().Add(3 * time.Second)
		return controlplane.SignExecutionGrant(priv, g, "boot", time.Now())
	}
	_, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, run, grant, pub, renew)
	require.NoError(t, err)
	require.True(t, hostRenewed)
	require.Equal(t, 1, c.starts)
}

func TestHostRelayRenewalFailureRequiresMatchingFinalEvidence(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "wrong_attempt"}[mismatch], func(t *testing.T) {
			pub, _, grant := relayGrant(t)
			run := controlplane.Run{ID: "run", Attempt: 1, Status: controlplane.RunDone}
			evidence := run
			if mismatch {
				evidence.Attempt++
			}
			c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
				return hostsupervisor.RunObservation{Status: "running"}, nil
			}, observe: func(context.Context) (hostsupervisor.RunObservation, error) { return relayEvidence(t, evidence), nil }}
			got, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, run, grant, pub, func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
				return controlplane.SignedExecutionGrant{}, errors.New("renew failed")
			})
			if mismatch {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, c.canceled)
		})
	}
}

func TestHostRelayCancellationUsesIndependentCleanup(t *testing.T) {
	pub, _, grant := relayGrant(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
		cancel()
		return hostsupervisor.RunObservation{Status: "running"}, nil
	}}
	got, err := awaitHostRun(ctx, c, hostsupervisor.RunRequest{}, controlplane.Run{}, grant, pub, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, got)
	require.Equal(t, 1, c.canceled)
}

func TestHostRelayTerminalEvidenceBindsRunIdentity(t *testing.T) {
	original := controlplane.Run{ID: "run", Topology: "top", Revision: "rev", OperationKey: "incarnation", RequestFingerprint: "fingerprint", Attempt: 2, LeaseOwner: "owner", AgentID: "agent", Target: "node", Status: controlplane.RunDone, QueuedAt: time.Now(), Op: "apply", Operation: "apply", SnapshotPath: "snapshot"}
	changes := map[string]func(*controlplane.Run){
		"id":                  func(r *controlplane.Run) { r.ID = "other" },
		"topology":            func(r *controlplane.Run) { r.Topology = "other" },
		"revision":            func(r *controlplane.Run) { r.Revision = "other" },
		"incarnation":         func(r *controlplane.Run) { r.OperationKey = "other" },
		"fingerprint":         func(r *controlplane.Run) { r.RequestFingerprint = "other" },
		"attempt":             func(r *controlplane.Run) { r.Attempt++ },
		"owner":               func(r *controlplane.Run) { r.LeaseOwner = "other" },
		"agent":               func(r *controlplane.Run) { r.AgentID = "other" },
		"target":              func(r *controlplane.Run) { r.Target = "other" },
		"missing_queued_at":   func(r *controlplane.Run) { r.QueuedAt = time.Time{} },
		"different_operation": func(r *controlplane.Run) { r.Operation = "destroy"; r.Op = "destroy" },
		"different_snapshot":  func(r *controlplane.Run) { r.SnapshotPath = "other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			other := original
			change(&other)
			got, err := terminalHostRun(relayEvidence(t, other), original)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestHostRelayRejectsInvalidObservationStatus(t *testing.T) {
	pub, _, grant := relayGrant(t)
	c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
		return hostsupervisor.RunObservation{Status: "unexpected"}, nil
	}, observe: func(context.Context) (hostsupervisor.RunObservation, error) {
		t.Fatal("invalid status must not be polled")
		return hostsupervisor.RunObservation{}, nil
	}, renew: func(controlplane.SignedExecutionGrant) error {
		t.Fatal("invalid status must not renew host")
		return nil
	}}
	got, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, controlplane.Run{}, grant, pub, func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
		t.Fatal("invalid status must not renew authority")
		return controlplane.SignedExecutionGrant{}, nil
	})
	require.Error(t, err)
	require.Nil(t, got)
	require.Equal(t, 1, c.starts)
	require.Equal(t, 1, c.canceled)
}

func TestHostRelayRejectsNonAdvancingRenewal(t *testing.T) {
	for _, olderIssue := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged_expiry", true: "older_issue"}[olderIssue], func(t *testing.T) {
			pub, priv, grant := relayGrant(t)
			c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
				return hostsupervisor.RunObservation{Status: "running"}, nil
			}, observe: func(context.Context) (hostsupervisor.RunObservation, error) {
				return hostsupervisor.RunObservation{Status: "uncertain"}, nil
			}, renew: func(controlplane.SignedExecutionGrant) error {
				t.Fatal("non-advancing renewal reached host")
				return nil
			}}
			got, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, controlplane.Run{}, grant, pub, func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
				if !olderIssue {
					return grant, nil
				}
				g := grant.Grant
				g.ExpiresAt = time.Now().Add(3 * time.Second)
				return controlplane.SignExecutionGrant(priv, g, "boot", grant.IssuedAt.Add(-time.Millisecond))
			})
			require.ErrorIs(t, err, controlplane.ErrInvalidExecutionGrant)
			require.Nil(t, got)
			require.Equal(t, 1, c.canceled)
		})
	}
}

func TestHostRelayRejectsRenewalIdentityChange(t *testing.T) {
	pub, priv, grant := relayGrant(t)
	c := &relayClient{start: func(context.Context) (hostsupervisor.RunObservation, error) {
		return hostsupervisor.RunObservation{Status: "running"}, nil
	}, observe: func(context.Context) (hostsupervisor.RunObservation, error) {
		return hostsupervisor.RunObservation{Status: "uncertain"}, nil
	}, renew: func(controlplane.SignedExecutionGrant) error { t.Fatal("changed grant reached host"); return nil }}
	got, err := awaitHostRun(context.Background(), c, hostsupervisor.RunRequest{}, controlplane.Run{}, grant, pub, func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
		g := grant.Grant
		g.SessionID = "replacement"
		g.ExpiresAt = time.Now().Add(3 * time.Second)
		return controlplane.SignExecutionGrant(priv, g, "boot", time.Now())
	})
	require.ErrorIs(t, err, controlplane.ErrInvalidExecutionGrant)
	require.Nil(t, got)
	require.Equal(t, 1, c.canceled)
}

func TestDecodeHostRunEvidenceRejectsNonTerminalEvidence(t *testing.T) {
	raw, err := json.Marshal(controlplane.Run{ID: "run-1", Status: controlplane.RunRunning})
	require.NoError(t, err)
	_, err = decodeHostRunEvidence(raw)
	require.Error(t, err)
}
