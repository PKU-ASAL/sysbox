package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type sshGuestConnection struct {
	*taskConnection
	result substrate.SSHAccessResult
	calls  int
	lost   bool
}

func (c *sshGuestConnection) DiscoverSSHAccess(context.Context, substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return substrate.SSHAccessDiscovery{}, substrate.ErrNotSupported
}
func (c *sshGuestConnection) InspectSSHAccess(context.Context, substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	return substrate.SSHAccessInspection{}, substrate.ErrNotSupported
}
func (c *sshGuestConnection) MutateSSHAccess(context.Context, substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	c.calls++
	if c.lost {
		return c.result, errors.New("response lost")
	}
	return c.result, nil
}
func (c *sshGuestConnection) ObserveSSHAccess(_ context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	if h != c.result.Handle {
		return substrate.SSHAccessResult{}, guesttask.ErrConflict
	}
	return c.result, nil
}
func sshGuestFixture(t *testing.T) (*SSHGuestDispatcher, TargetRef, substrate.SSHAccessMutation, controlplane.SignedExecutionGrant, *sshGuestConnection) {
	t.Helper()
	d, old, conn, key := taskFixtureWithIssuer(t)
	req := substrate.SSHAccessMutation{ID: "access-mutation", ExpectedRevision: 1, Delete: true, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := req.Digest()
	require.NoError(t, err)
	g := old.Grant.Grant
	g.ActionID = req.ID
	g.Operation = "guest_ssh_access_delete"
	g.Digest = digest
	signed, err := controlplane.SignExecutionGrant(key, g, g.TargetBootID, time.Now())
	require.NoError(t, err)
	ref := testRef(old)
	c := &sshGuestConnection{taskConnection: conn, result: substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{NodeID: "provider-id", ID: req.ID, Digest: digest, User: "ubuntu", AccessID: "admin", Revision: 2, BootID: g.TargetBootID, TopologyUID: g.Scope.TopologyUID, ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation, Epoch: g.Epoch}}}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "provider-id", User: "ubuntu", Connection: c}, nil
	}
	return &SSHGuestDispatcher{Dispatcher: d, Report: func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		return nil
	}}, ref, req, signed, c
}
func TestSSHGuestRejectsRevokedAuthority(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	d.Authorize = func(context.Context, controlplane.SignedExecutionGrant) error { return errors.New("revoked") }
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	require.Zero(t, c.calls)
}
func TestSSHGuestCorruptIntentIsNotReplayed(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	h := c.result.Handle
	h.NodeID = ref.Node
	path := sshGuestPath(d.Root, h)
	require.NoError(t, os.WriteFile(path, []byte("broken"), 0600))
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	require.Zero(t, c.calls)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "broken", string(raw))
}
func TestSSHGuestRecoveryUsesProviderIdentity(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	c.lost = true
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	cold := *d
	require.NoError(t, cold.Recover(context.Background()))
	require.Equal(t, 1, c.calls)
}
func TestSSHGuestRetriesExactUnacknowledgedReceipt(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	var receipts []controlplane.SignedExecutionStopReceipt
	d.Report = func(_ context.Context, _ string, r controlplane.SignedExecutionStopReceipt, _ json.RawMessage) error {
		receipts = append(receipts, r)
		if len(receipts) == 1 {
			return errors.New("ack lost")
		}
		return nil
	}
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	_, err = d.Mutate(context.Background(), ref, req, g)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	first, err := json.Marshal(receipts[0])
	require.NoError(t, err)
	second, err := json.Marshal(receipts[1])
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
	require.Equal(t, 1, c.calls)
}
func TestSSHGuestRejectsForeignProviderResult(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	c.result.Handle.NodeID = "foreign"
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
}
func TestSSHGuestJournalFilenameBounded(t *testing.T) {
	h := substrate.SSHAccessHandle{TopologyUID: strings.Repeat("a", 128), ResourceUID: strings.Repeat("b", 128), ID: strings.Repeat("c", 128)}
	require.Less(t, len(filepath.Base(sshGuestPath("/tmp", h))), 100)
}

func TestSSHGuestConcurrentDispatchAndRecovery(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cold := *d
			_, err := cold.Mutate(context.Background(), ref, req, g)
			failures <- err
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); cold := *d; failures <- cold.Recover(context.Background()) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Equal(t, 1, c.calls)
	require.NoError(t, d.Recover(context.Background()))
}

func TestSSHGuestLargeValidIntentRemainsRecoverable(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	public, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	value := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))) + " " + strings.Repeat("<", 16000)
	req.Delete = false
	req.Config.AuthorizedKeys = make([]string, 64)
	for i := range req.Config.AuthorizedKeys {
		req.Config.AuthorizedKeys[i] = value
	}
	g.Grant.Operation = "guest_ssh_access_apply"
	g.Grant.Digest, err = req.Digest()
	require.NoError(t, err)
	g, err = controlplane.SignExecutionGrant(key, g.Grant, g.BootID, time.Now())
	require.NoError(t, err)
	d.IssuerKey = pub
	c.result.Handle.Digest = g.Grant.Digest
	c.result.Present = true
	c.lost = true
	_, err = d.Mutate(context.Background(), ref, req, g)
	require.ErrorContains(t, err, "response lost")
	require.NoError(t, d.Recover(context.Background()))
	require.Equal(t, 1, c.calls)
}

func TestSSHGuestRecoveryContinuesPastCorruption(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	c.lost = true
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(d.Root, "000.json"), []byte("broken"), 0600))
	reports := 0
	d.Report = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		reports++
		return nil
	}
	require.Error(t, d.Recover(context.Background()))
	require.Equal(t, 1, reports)
	require.Equal(t, 1, c.calls)
}

func TestSSHGuestRecoveryRejectsReplacedProvider(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	c.lost = true
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, r TargetRef) (Target, error) {
		target, err := resolve(ctx, r)
		target.NodeID = "replacement"
		return target, err
	}
	require.Error(t, d.Recover(context.Background()))
	require.Equal(t, 1, c.calls)
}

func TestSSHGuestTamperedJournalRejected(t *testing.T) {
	for _, field := range []string{"version", "ref", "request", "grant", "receipt", "result", "provider", "symlink"} {
		t.Run(field, func(t *testing.T) {
			d, ref, req, g, c := sshGuestFixture(t)
			result, err := d.Mutate(context.Background(), ref, req, g)
			require.NoError(t, err)
			path := sshGuestPath(d.Root, result.Handle)
			r, err := d.loadSSH(path)
			require.NoError(t, err)
			switch field {
			case "version":
				r.Version++
			case "ref":
				r.Ref.Node = "other"
			case "request":
				r.Request.Config.User = "root"
			case "grant":
				r.Grant.Grant.AgentID = "other"
			case "receipt":
				r.Receipt.Signature[0] ^= 1
			case "result":
				r.Result.Handle.Generation++
			case "provider":
				r.ProviderNodeID = ""
			}
			require.NoError(t, d.saveSSH(r))
			if field == "symlink" {
				require.NoError(t, os.Rename(path, path+".original"))
				require.NoError(t, os.Symlink(path+".original", path))
			}
			_, err = d.Mutate(context.Background(), ref, req, g)
			require.Error(t, err)
			require.Equal(t, 1, c.calls)
		})
	}
}

func TestSSHGuestNativeMutationRPC(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	raw, err := json.Marshal(map[string]any{"version": 1, "operation": "ssh-guest-mutate", "target": ref, "ssh_mutation": req, "grant": g})
	require.NoError(t, err)
	var request Request
	require.NoError(t, json.Unmarshal(raw, &request))
	response := (&Server{SSHGuest: d}).Handle(context.Background(), request)
	require.Empty(t, response.Error)
	require.Equal(t, 1, c.calls)
	request.Operation = "ssh-guest-observe"
	response = (&Server{SSHGuest: d}).Handle(context.Background(), request)
	require.Empty(t, response.Error)
	require.Equal(t, 1, c.calls)
}

func TestSSHGuestServerRecoversOnStartup(t *testing.T) {
	d, ref, req, g, c := sshGuestFixture(t)
	c.lost = true
	_, err := d.Mutate(context.Background(), ref, req, g)
	require.Error(t, err)
	var reports atomic.Int32
	d.Report = func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error {
		reports.Add(1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{Socket: filepath.Join(t.TempDir(), "host.sock"), GroupID: -1, SSHGuest: d}
	require.NoError(t, os.Chmod(filepath.Dir(server.Socket), 0700))
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	require.Eventually(t, func() bool { return reports.Load() == 1 }, time.Second, 10*time.Millisecond)
}
