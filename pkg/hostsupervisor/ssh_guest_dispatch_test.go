package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

type managedSSHTestConn struct {
	taskConnection
	result    substrate.SSHAccessResult
	mutations int
	lost      bool
	unknown   bool
}

func sshMutationHandle(ref TargetRef, req substrate.SSHAccessMutation, g controlplane.SignedExecutionGrant) substrate.SSHAccessHandle {
	return substrate.SSHAccessHandle{ID: req.ID, Digest: g.Grant.Digest, NodeID: ref.Node, User: req.Config.User, AccessID: req.Config.AccessID, Revision: req.ExpectedRevision + 1, BootID: g.Grant.TargetBootID, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: g.Grant.Epoch}
}
func (c *managedSSHTestConn) MutateSSHAccess(ctx context.Context, req substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	c.mutations++
	_, g, _ := transport.TaskGrantFromContext(ctx)
	c.result = substrate.SSHAccessResult{Present: !req.Delete, Handle: sshMutationHandle(TargetRef{Node: "provider", Scope: g.Grant.Scope}, req, g)}
	if c.lost {
		return substrate.SSHAccessResult{}, errors.New("lost reply")
	}
	return c.result, nil
}
func (c *managedSSHTestConn) ObserveSSHAccess(context.Context, substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	if c.unknown {
		return substrate.SSHAccessResult{}, guesttask.ErrUnknown
	}
	return c.result, nil
}
func (c *managedSSHTestConn) InspectSSHAccess(context.Context, substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return substrate.SSHAccessInspection{Status: "absent", Revision: c.result.Handle.Revision}, nil
}
func (c *managedSSHTestConn) DiscoverSSHAccess(context.Context, substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return substrate.SSHAccessDiscovery{Status: "absent", BootID: c.result.Handle.BootID}, nil
}

func TestGuestSSHDispatcherColdRecoveryDoesNotRepeatMutation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "unknown"}[unknown], func(t *testing.T) {
			d, _, _, issuer := taskFixtureWithIssuer(t)
			req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 2, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
			digest, err := req.Digest()
			require.NoError(t, err)
			ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}}
			grant, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "guest-boot", AgentID: d.AgentID, SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 7, Operation: "guest_ssh_access_delete", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "guest-boot", time.Now())
			require.NoError(t, err)
			conn := &managedSSHTestConn{lost: true, unknown: unknown}
			d.Resolve = func(context.Context, TargetRef) (Target, error) {
				return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "provider", User: "ubuntu", Connection: conn}, nil
			}
			d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error { return nil }
			reports := 0
			ssh := &SSHGuestDispatcher{Dispatcher: d, Report: func(_ context.Context, id string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
				reports++
				require.Equal(t, req.ID, id)
				return controlplane.VerifyGuestSSHAccessStopReceipt(d.HostKey.Public().(ed25519.PublicKey), r, req, "web")
			}}
			_, err = ssh.Mutate(context.Background(), ref, req, grant)
			t.Logf("mutate error: %v", err)
			require.Error(t, err)
			require.Equal(t, 1, conn.mutations)
			require.Zero(t, reports)
			conn.lost = false
			cold := *ssh
			copyDispatcher := *d
			cold.Dispatcher = &copyDispatcher
			cold.HostBootID = "new-host-boot"
			err = cold.Recover(context.Background())
			if unknown {
				require.Error(t, err)
				require.Zero(t, reports)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, reports)
				require.NoError(t, cold.Recover(context.Background()))
				require.Equal(t, 1, reports)
			}
			require.Equal(t, 1, conn.mutations)
			_, err = cold.Mutate(context.Background(), ref, req, grant)
			if unknown {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, conn.mutations)
		})
	}
}

func TestGuestSSHDispatcherRejectsInvalidSignatureBeforePersistence(t *testing.T) {
	d, _, _, _ := taskFixtureWithIssuer(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}}
	g, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "guest", AgentID: d.AgentID, SessionID: "session", CommandID: "cmd", ActionID: req.ID, Attempt: 1, Epoch: 1, Operation: "guest_ssh_access_delete", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "guest", time.Now())
	require.NoError(t, err)
	ssh := &SSHGuestDispatcher{Dispatcher: d, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		t.Fatal("reported forged grant")
		return nil
	}}
	_, err = ssh.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
}

func TestDerivedSSHColdRecoveryUsesPersistedRunAndDedicatedReport(t *testing.T) {
	d, _, _, issuer := taskFixtureWithIssuer(t)
	d.RunID = "run"
	req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 2, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}}
	grant, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "guest-boot", AgentID: d.AgentID, SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 7, ParentEpoch: 3, Operation: "guest_ssh_access_delete", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "guest-boot", time.Now())
	require.NoError(t, err)
	conn := &managedSSHTestConn{lost: true}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{}, errors.New("standalone resolver forbidden")
	}
	d.ResolveRun = func(_ context.Context, got TargetRef, run string) (Target, error) {
		require.Equal(t, "run", run)
		require.Equal(t, ref, got)
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "provider", User: "ubuntu", Connection: conn}, nil
	}
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error { return nil }
	reports := 0
	ssh := &SSHGuestDispatcher{Dispatcher: d, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		t.Fatal("standalone report forbidden")
		return nil
	}, ReportDerived: func(_ context.Context, got substrate.SSHAccessMutation, r controlplane.SignedExecutionStopReceipt) error {
		reports++
		require.Equal(t, req, got)
		return controlplane.VerifyGuestSSHAccessStopReceipt(d.HostKey.Public().(ed25519.PublicKey), r, req, "web")
	}}
	_, err = ssh.Mutate(context.Background(), ref, req, grant)
	require.Error(t, err)
	require.Equal(t, 1, conn.mutations)
	cold := *ssh
	dispatcher := *d
	dispatcher.RunID = ""
	cold.Dispatcher = &dispatcher
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
	require.Equal(t, 1, conn.mutations)
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
}

type stopManagedSSHConn struct {
	*managedSSHTestConn
	stops int
}

func (c *stopManagedSSHConn) StopSSHAccess(ctx context.Context, req substrate.SSHAccessMutation) (substrate.SSHAccessStop, error) {
	c.stops++
	_, g, _ := transport.TaskGrantFromContext(ctx)
	return substrate.SSHAccessStop{Handle: sshMutationHandle(TargetRef{Node: "provider", Scope: g.Grant.Scope}, req, g), Quiescent: true, Outcome: "unknown"}, nil
}
func TestDerivedSSHUnknownRecoveryStopsWithoutReplay(t *testing.T) {
	d, _, _, issuer := taskFixtureWithIssuer(t)
	d.RunID = "run"
	req := substrate.SSHAccessMutation{ID: "ssh-stop", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}}
	grant, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "guest-boot", AgentID: d.AgentID, SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 7, ParentEpoch: 3, Operation: "guest_ssh_access_delete", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "guest-boot", time.Now())
	require.NoError(t, err)
	conn := &stopManagedSSHConn{managedSSHTestConn: &managedSSHTestConn{lost: true, unknown: true}}
	d.ResolveRun = func(context.Context, TargetRef, string) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "provider", User: "ubuntu", Connection: conn}, nil
	}
	reports := 0
	var receipt controlplane.SignedExecutionStopReceipt
	dispatcher := &SSHGuestDispatcher{Dispatcher: d, ReportDerived: func(_ context.Context, got substrate.SSHAccessMutation, r controlplane.SignedExecutionStopReceipt) error {
		reports++
		receipt = r
		return controlplane.VerifyGuestSSHUnknownStopReceipt(d.HostKey.Public().(ed25519.PublicKey), r, got, "web")
	}}
	_, err = dispatcher.Mutate(context.Background(), ref, req, grant)
	require.Error(t, err)
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error { return ErrGrantObsolete }
	cold := *dispatcher
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, conn.stops)
	require.Equal(t, 1, conn.mutations)
	require.Equal(t, 1, reports)
	require.NotNil(t, receipt.SSHStop)
	require.Nil(t, receipt.SSHResult)
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, reports)
	_, err = cold.Observe(context.Background(), ref, req, grant)
	require.ErrorIs(t, err, guesttask.ErrUnknown)
}

func TestSSHAuthorityUnavailableDoesNotRetireDerivedRequest(t *testing.T) {
	d, _, _, issuer := taskFixtureWithIssuer(t)
	d.RunID = "run"
	req := substrate.SSHAccessMutation{ID: "unavailable", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}}
	grant, err := controlplane.SignExecutionGrant(issuer, controlplane.ExecutionGrant{Scope: ref.Scope, TargetBootID: "guest-boot", AgentID: d.AgentID, SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: 7, ParentEpoch: 3, Operation: "guest_ssh_access_delete", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, "guest-boot", time.Now())
	require.NoError(t, err)
	conn := &stopManagedSSHConn{managedSSHTestConn: &managedSSHTestConn{lost: true, unknown: true}}
	d.ResolveRun = func(context.Context, TargetRef, string) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "provider", User: "ubuntu", Connection: conn}, nil
	}
	reports := 0
	dispatcher := &SSHGuestDispatcher{Dispatcher: d, ReportDerived: func(context.Context, substrate.SSHAccessMutation, controlplane.SignedExecutionStopReceipt) error {
		reports++
		return nil
	}}
	_, err = dispatcher.Mutate(context.Background(), ref, req, grant)
	require.Error(t, err)
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error {
		return errors.New("authority HTTP 500 unavailable")
	}
	require.Error(t, dispatcher.Recover(context.Background()))
	require.Zero(t, conn.stops)
	require.Zero(t, reports)
}
