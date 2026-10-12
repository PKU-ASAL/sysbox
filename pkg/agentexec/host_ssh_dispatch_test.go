package agentexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

type runSSHNode struct {
	driver.Node
	driver.NodeState
	connection substrate.Connection
}

func (n runSSHNode) UnmarshalProviderState(json.RawMessage) (any, error) { return nil, nil }
func (n runSSHNode) ObserveNode(ctx context.Context, _ substrate.NodeHandle) (substrate.NodeObservation, error) {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	return substrate.NodeObservation{Exists: true, OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: id.Generation}, nil
}
func (n runSSHNode) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	return n.connection, nil
}

type runSSHConnection struct {
	substrate.Connection
	substrate.ConnectionSSHAccess
	t         *testing.T
	mutations int
}

func (c *runSSHConnection) DiscoverSSHAccess(_ context.Context, q substrate.SSHAccessQuery) (substrate.SSHAccessDiscovery, error) {
	return substrate.SSHAccessDiscovery{Query: q, BootID: "guest", Status: "absent"}, nil
}
func (c *runSSHConnection) MutateSSHAccess(ctx context.Context, r substrate.SSHAccessMutation) (substrate.SSHAccessResult, error) {
	c.mutations++
	id, g, ok := transport.TaskGrantFromContext(ctx)
	require.True(c.t, ok)
	require.Equal(c.t, r.ID, id)
	require.Equal(c.t, uint64(3), g.Grant.ParentEpoch)
	return substrate.SSHAccessResult{Present: !r.Delete, Handle: substrate.SSHAccessHandle{ID: r.ID, Digest: g.Grant.Digest, NodeID: "provider", BootID: "guest", User: r.Config.User, AccessID: r.Config.AccessID, Revision: r.ExpectedRevision + 1, TopologyUID: g.Grant.Scope.TopologyUID, ResourceUID: g.Grant.Scope.ResourceUID, Generation: g.Grant.Scope.Generation, Epoch: g.Grant.Epoch}}, nil
}

func TestHostRunSSHDispatchUsesCanonicalCandidateAndSignedChild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "state.json")
	repo, err := persistence.OpenConfigured(ctx, path, path)
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	conn := &runSSHConnection{t: t}
	node := runSSHNode{connection: conn}
	name := "run-ssh-" + filepath.Base(filepath.Dir(dir))
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: name, Version: "1", Node: node, NodeState: node}))
	action := runtime.ActionRecord{ID: "create", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
	require.NoError(t, repo.CommitActionIntent(ctx, action))
	action.Revision = 1
	action.Status = runtime.ActionUncertain
	action.Candidate = &state.Resource{Address: action.Resource, Driver: name, ExternalID: "provider", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []config.ConnectionConfig{{User: "ubuntu"}}}})}
	require.NoError(t, repo.CommitActionOutcome(ctx, action, nil))
	public, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, host, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	parent := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: st.TopologyUID}, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "run", Attempt: 1, Epoch: 3, Operation: "run_apply", Digest: "run", TargetBootID: "host", State: controlplane.GrantActive, ExpiresAt: time.Now().UTC().Add(time.Minute)}
	signed, err := controlplane.SignExecutionGrant(issuer, parent, "host", time.Now())
	require.NoError(t, err)
	reports := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/agent/execution-grant/derive-ssh":
			var body struct {
				Parent      controlplane.SignedExecutionGrant `json:"parent"`
				Request     substrate.SSHAccessMutation       `json:"request"`
				ResourceUID string                            `json:"resource_uid"`
				Generation  int64                             `json:"generation"`
				GuestBootID string                            `json:"guest_boot_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, parent, body.Parent.Grant)
			require.Equal(t, "node", body.ResourceUID)
			require.Equal(t, "guest", body.GuestBootID)
			digest, e := body.Request.Digest()
			require.NoError(t, e)
			child := parent
			child.ParentEpoch = parent.Epoch
			child.Epoch = 4
			child.Scope.ResourceUID = "node"
			child.Scope.Generation = 1
			child.ActionID = body.Request.ID
			child.Operation = "guest_ssh_access_delete"
			child.Digest = digest
			child.TargetBootID = "guest"
			proof, e := controlplane.SignExecutionGrant(issuer, child, "guest", time.Now())
			require.NoError(t, e)
			require.NoError(t, json.NewEncoder(w).Encode(proof))
		case "/v1/hosts/agent/derived-ssh/ssh/completed":
			var body struct {
				Request substrate.SSHAccessMutation             `json:"request"`
				Receipt controlplane.SignedExecutionStopReceipt `json:"receipt"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.NoError(t, controlplane.VerifyGuestSSHAccessStopReceipt(host.Public().(ed25519.PublicKey), body.Receipt, body.Request, "web"))
			reports++
		default:
			t.Errorf("unexpected authority path %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer api.Close()
	authority := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: api.URL, Secret: "secret"}}
	journal := t.TempDir()
	require.NoError(t, os.Chmod(journal, 0700))
	dispatcher := &hostsupervisor.Dispatcher{Root: journal, AgentID: "agent", HostBootID: "host", IssuerKey: public, HostKey: host, Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }}
	executor := &HostRunExecutor{Resolver: hostsupervisor.CanonicalResolver{Backend: path, AgentID: "agent"}, Authority: authority, SSH: &hostsupervisor.SSHGuestDispatcher{Dispatcher: dispatcher, ReportDerived: authority.ReportDerivedSSHAccess}}
	ctx = executor.withRunSSH(transport.WithParentExecutionGrant(ctx, signed), hostsupervisor.RunRequest{RunID: "run", Topology: "lab"})
	ctx = substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "ssh", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1})
	result, err := transport.NewTaskClient(nil, "provider", 1).MutateSSHAccess(ctx, substrate.SSHAccessMutation{ID: "ssh", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}})
	require.NoError(t, err)
	require.Equal(t, "provider", result.Handle.NodeID)
	require.Equal(t, 1, conn.mutations)
	require.Equal(t, 1, reports)
}
