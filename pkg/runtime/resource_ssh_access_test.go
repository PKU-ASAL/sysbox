package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimeSSHConnection struct {
	*recordingConn
	found substrate.SSHAccessDiscovery
	calls []substrate.SSHAccessMutation
	lost  bool
}

func (c *runtimeSSHConnection) DiscoverSSHAccess(_ context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	r := c.found
	r.Query = q
	return r, nil
}
func (c *runtimeSSHConnection) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	c.calls = append(c.calls, r)
	digest, _ := r.Digest()
	result := substrate.SSHAccessResult{Present: !r.Delete, Handle: substrate.SSHAccessHandle{NodeID: "provider-node", ID: r.ID, Digest: digest, User: r.Config.User, AccessID: r.Config.AccessID, Revision: r.ExpectedRevision + 1, BootID: "guest", TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation, Epoch: 1}}
	c.found = substrate.SSHAccessDiscovery{BootID: "guest", Status: "present", Current: &result, Config: &r.Config}
	if r.Delete {
		c.found.Status = "absent"
	}
	if c.lost {
		return result, errors.New("reply lost")
	}
	return result, nil
}
func (c *runtimeSSHConnection) ObserveSSHAccess(context.Context, substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	return *c.found.Current, nil
}
func (c *runtimeSSHConnection) InspectSSHAccess(_ context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	status := c.found.Status
	if c.found.Current != nil && c.found.Current.Handle.Revision != h.Revision && status == "present" {
		status = "drifted"
	}
	return substrate.SSHAccessInspection{Status: status, Revision: c.found.Current.Handle.Revision}, nil
}
func runtimeSSHFixture(t *testing.T) (context.Context, *ProviderContext, *graph.Node, *runtimeSSHConnection) {
	t.Helper()
	e, d, _, nodeAddr := nodeGuardExecutor(t, nil)
	e.state.TopologyUID = "owner"
	node := state.Resource{Address: nodeAddr, ResourceType: "sysbox_node", Driver: d.name, ExternalID: "provider-node", OwnerUID: "owner", ResourceUID: "node-uid", Generation: 2, Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []config.ConnectionConfig{{User: "ubuntu"}}}})}
	e.state.AddResource(node)
	conn := &runtimeSSHConnection{recordingConn: &recordingConn{}, found: substrate.SSHAccessDiscovery{BootID: "guest", Status: "absent"}}
	d.guestConn = conn
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	access := address.Resource("sysbox_ssh_access", "admin")
	require.NoError(t, e.graph.AddNode(access, nil))
	require.NoError(t, e.graph.SetData(access, &config.SSHAccessConfig{Node: nodeAddr.String(), AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}}))
	ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "create-access", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "access-uid", Generation: 1})
	stored, err := cloneExecutionState(e.state)
	require.NoError(t, err)
	e.actions.(*memoryActions).state = stored
	return ctx, &ProviderContext{exec: e}, e.graph.Get(access), conn
}

func TestSSHAccessRuntimeActionRecoversLostReplyWithoutReplay(t *testing.T) {
	_, pc, n, conn := runtimeSSHFixture(t)
	conn.lost = true
	e := pc.exec
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(n.Address, controlplane.PlanActionCreate)))
	require.Len(t, conn.calls, 1)
	cold := NewExecutor(e.graph, e.state)
	cold.SetRunContext(e.topology, "cold-run")
	cold.SetActionRepository(e.actions)
	require.NoError(t, cold.Apply(context.Background(), singleConvergencePlan(n.Address, controlplane.PlanActionCreate)))
	require.Len(t, conn.calls, 1)
	require.NotNil(t, cold.state.FindResource(n.Address))
}

func TestSSHAccessUsesStateAwareObservationBoundary(t *testing.T) {
	_, ok := any(SSHAccessResourceHandler{}).(StateAwareResourceReader)
	require.True(t, ok)
}
func TestSSHAccessRuntimeCreateReadDestroyWithoutHCL(t *testing.T) {
	ctx, pc, n, conn := runtimeSSHFixture(t)
	current, err := (SSHAccessResourceHandler{}).Create(ctx, pc, n)
	require.NoError(t, err)
	require.Len(t, conn.calls, 1)
	require.Equal(t, "create-access", conn.calls[0].ID)
	require.Equal(t, uint64(0), conn.calls[0].ExpectedRevision)
	require.Equal(t, "node-uid", conn.found.Current.Handle.ResourceUID)
	require.Equal(t, "access-uid", current.ResourceUID)
	read, err := readResourceWithAttachments(ctx, SSHAccessResourceHandler{}, current, pc.State())
	require.NoError(t, err)
	require.Equal(t, state.ResourcePresent, read.Status)
	pc.exec.graph = graph.New()
	ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "delete-access", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "access-uid", Generation: 1})
	require.NoError(t, (SSHAccessResourceHandler{}).Delete(ctx, pc, current))
	require.Len(t, conn.calls, 2)
	require.True(t, conn.calls[1].Delete)
	require.Equal(t, uint64(1), conn.calls[1].ExpectedRevision)
	read, err = readResourceWithAttachments(ctx, SSHAccessResourceHandler{}, current, pc.State())
	require.NoError(t, err)
	require.Equal(t, state.ResourceAbsent, read.Status)
}
func TestSSHAccessRuntimeRefusesNewerRevisionDelete(t *testing.T) {
	ctx, pc, n, conn := runtimeSSHFixture(t)
	current, err := (SSHAccessResourceHandler{}).Create(ctx, pc, n)
	require.NoError(t, err)
	conn.found.Current.Handle.Revision++
	require.Error(t, (SSHAccessResourceHandler{}).Delete(ctx, pc, current))
	require.Len(t, conn.calls, 1)
}
func TestSSHAccessRuntimeLostReplyHasRecoverableObservation(t *testing.T) {
	ctx, pc, n, conn := runtimeSSHFixture(t)
	conn.lost = true
	_, err := (SSHAccessResourceHandler{}).Create(ctx, pc, n)
	require.Error(t, err)
	discovery, ok := any(SSHAccessResourceHandler{}).(ResourceDiscoverer)
	require.True(t, ok)
	result, err := discovery.Discover(ctx, pc, n)
	require.NoError(t, err)
	require.Equal(t, state.ResourcePresent, result.Status)
	require.Len(t, conn.calls, 1)
}
