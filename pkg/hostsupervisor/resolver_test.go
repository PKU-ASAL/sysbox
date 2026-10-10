package hostsupervisor

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type candidateNode struct {
	driver.Node
	driver.NodeState
	observations, connections int
	wrongIdentity             bool
	connection                substrate.Connection
}

func (n *candidateNode) UnmarshalProviderState(json.RawMessage) (any, error) { return nil, nil }
func (n *candidateNode) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	n.observations++
	id, _ := substrate.OperationIdentityFromContext(ctx)
	if n.wrongIdentity {
		id.ResourceUID = "foreign"
	}
	return substrate.NodeObservation{Exists: true, OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: id.Generation}, nil
}
func (n *candidateNode) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	n.connections++
	return n.connection, nil
}

func TestResolveRunTaskVerifiesPersistedCandidateBeforeConnecting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "state.json")
	repo, err := persistence.OpenConfigured(ctx, path, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repo.Close()) })
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	node := &candidateNode{}
	name := "candidate-resolver-" + filepath.Base(filepath.Dir(dir))
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: name, Version: "1", Node: node, NodeState: node}))
	a := runtime.ActionRecord{ID: "create", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
	require.NoError(t, repo.CommitActionIntent(ctx, a))
	a.Revision = 1
	a.Status = runtime.ActionUncertain
	a.Candidate = &state.Resource{Address: a.Resource, Driver: name, ExternalID: "actual-node", OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: 1}
	require.NoError(t, repo.CommitActionOutcome(ctx, a, nil))
	r := CanonicalResolver{Backend: path, AgentID: "agent"}
	scope := controlplane.ExecutionScope{TopologyUID: st.TopologyUID, ResourceUID: "node", Generation: 1}
	ref, err := r.FindRunTaskRef(ctx, "lab", "run", scope)
	require.NoError(t, err)
	require.Equal(t, "web", ref.Node)
	_, err = r.ResolveRunTask(ctx, ref, "foreign-run")
	require.Error(t, err)
	require.Zero(t, node.observations)
	got, err := r.ResolveRunTask(ctx, ref, "run")
	require.NoError(t, err)
	require.Equal(t, "actual-node", got.NodeID)
	require.Equal(t, 1, node.connections)
	node.wrongIdentity = true
	_, err = r.ResolveRunTask(ctx, ref, "run")
	require.Error(t, err)
	require.Equal(t, 1, node.connections, "identity mismatch must not reach connection")
	_, err = r.Resolve(ctx, ref)
	require.Error(t, err, "ordinary tasks cannot resolve pending Run checkpoints")

	// A fresh global dispatcher has no parent closure. Its persisted Run ID
	// must recover the pending node through the separately configured resolver.
	node.wrongIdentity = false
	d, req, conn, issuer := taskFixtureWithIssuer(t)
	node.connection = conn
	r.AgentID = d.AgentID
	req.Grant.Grant.Scope = scope
	req.Grant.Grant.ParentEpoch = 7
	req.Grant, err = controlplane.SignExecutionGrant(issuer, req.Grant.Grant, req.Grant.BootID, time.Now())
	require.NoError(t, err)
	conn.handle.TopologyUID = scope.TopologyUID
	conn.handle.ResourceUID = scope.ResourceUID
	conn.handle.NodeID = "actual-node"
	d.RunID = "run"
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) { return r.ResolveRunTask(ctx, ref, "run") }
	d.ResolveRun = r.ResolveRunTask
	record, err := d.Start(ctx, ref, req)
	require.NoError(t, err)
	require.Equal(t, "run", record.RunID)
	cold := *d
	cold.RunID = ""
	cold.Resolve = r.Resolve
	require.NoError(t, cold.Recover(ctx))
	_, err = cold.Observe(ctx, ref, record.Handle)
	require.NoError(t, err)
	_, err = cold.Renew(ctx, ref, record.Handle, req.Grant)
	require.NoError(t, err)
	_, err = cold.Cancel(ctx, ref, record.Handle)
	require.NoError(t, err)
	require.Equal(t, 1, conn.starts, "cold recovery must never launch again")
	record.RunID = "foreign-run"
	require.NoError(t, cold.save(record))
	_, err = cold.Observe(ctx, ref, record.Handle)
	require.Error(t, err, "foreign Run must not reach the pending candidate")
	record.RunID = ""
	require.NoError(t, cold.save(record))
	_, err = cold.Observe(ctx, ref, record.Handle)
	require.Error(t, err, "derived records must retain trusted Run provenance")

	// API discovery must also work with no task record and only an unfinished
	// canonical node candidate. It may not borrow a different Run's candidate.
	cold.Root = t.TempDir()
	require.NoError(t, os.Chmod(cold.Root, 0700))
	cold.FindRunTaskRef = r.FindRunTaskRef
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	wrong, err := controlplane.SignRunExecutionCancellation(issuer, g, "foreign-run", "lab")
	require.NoError(t, err)
	connections := node.connections
	_, err = cold.StopAuthorized(ctx, controlplane.GuestTaskStop{Topology: "lab", Cancellation: wrong})
	require.Error(t, err)
	require.Equal(t, connections, node.connections)
	proof, err := controlplane.SignRunExecutionCancellation(issuer, g, "run", "lab")
	require.NoError(t, err)
	conn.result.RemoteTermination = substrate.RemoteTerminationConfirmed
	recovered, err := cold.StopAuthorized(ctx, controlplane.GuestTaskStop{Topology: "lab", Cancellation: proof})
	require.NoError(t, err)
	require.True(t, recovered.Reported)
	require.Equal(t, "actual-node", recovered.ProviderNodeID)
	require.Equal(t, "web", recovered.Ref.Node)
	require.Equal(t, 1, conn.starts)
	mapped := r
	mapped.ActionRun = func(_ context.Context, topology, execution string) (string, error) {
		if topology != "lab" || execution != "continuation" {
			return "", persistence.ErrConflict
		}
		return "run", nil
	}
	ref, err = mapped.FindRunTaskRef(ctx, "lab", "continuation", scope)
	require.NoError(t, err)
	_, err = mapped.ResolveRunTask(ctx, ref, "continuation")
	require.NoError(t, err)
	priorConnections := node.connections
	_, err = mapped.ResolveRunTask(ctx, ref, "unrelated")
	require.Error(t, err)
	require.Equal(t, priorConnections, node.connections)
	// Persist the execution Run, not the original action owner. A fresh
	// dispatcher must resolve that same mapping for stop recovery.
	d.Root = t.TempDir()
	require.NoError(t, os.Chmod(d.Root, 0700))
	d.RunID = "continuation"
	d.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		return mapped.ResolveRunTask(ctx, ref, "continuation")
	}
	d.ResolveRun = mapped.ResolveRunTask
	started, err := d.Start(ctx, ref, req)
	require.NoError(t, err)
	require.Equal(t, "continuation", started.RunID)
	coldMapped := *d
	coldMapped.RunID = ""
	coldMapped.Resolve = mapped.Resolve
	_, err = coldMapped.Cancel(ctx, ref, started.Handle)
	require.NoError(t, err)
}

func TestRunTaskResourceSelection(t *testing.T) {
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 2}}
	candidate := state.Resource{Address: address.Resource("sysbox_node", "web"), OwnerUID: "topology", ResourceUID: "node", Generation: 2}
	action := runtime.ActionRecord{Topology: "lab", RunID: "run", Resource: candidate.Address, Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: "topology", ResourceUID: "node", Generation: 2, Candidate: &candidate}
	for _, tc := range []struct {
		name   string
		change func(*state.State, *[]runtime.ActionRecord)
		ok     bool
	}{
		{"pending candidate", func(*state.State, *[]runtime.ActionRecord) {}, true},
		{"missing candidate", func(_ *state.State, a *[]runtime.ActionRecord) { (*a)[0].Candidate = nil }, false},
		{"other run", func(_ *state.State, a *[]runtime.ActionRecord) { (*a)[0].RunID = "other" }, false},
		{"ambiguous", func(_ *state.State, a *[]runtime.ActionRecord) { *a = append(*a, (*a)[0]) }, false},
		{"stale", func(s *state.State, _ *[]runtime.ActionRecord) {
			r := candidate
			r.Generation = 3
			s.Resources = []state.Resource{r}
		}, false},
		{"canonical", func(s *state.State, a *[]runtime.ActionRecord) { s.Resources = []state.Resource{candidate}; *a = nil }, true},
		{"reset successor", func(s *state.State, a *[]runtime.ActionRecord) {
			r := candidate
			r.Generation = 1
			s.Resources = []state.Resource{r}
			(*a)[0].Action = controlplane.PlanActionReset
			(*a)[0].EffectComplete = true
		}, true},
		{"reset old checkpoint", func(s *state.State, a *[]runtime.ActionRecord) {
			r := candidate
			r.Generation = 1
			s.Resources = []state.Resource{r}
			(*a)[0].Action = controlplane.PlanActionReset
			(*a)[0].Candidate = &r
		}, false},
		{"reset replacement before initialization completes", func(s *state.State, a *[]runtime.ActionRecord) {
			old := candidate
			old.Generation = 1
			s.Resources = []state.Resource{old}
			(*a)[0].Action, (*a)[0].Phase = controlplane.PlanActionReset, "applying"
			(*a)[0].Candidate = &old
			(*a)[0].Replacement = &candidate
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.State{TopologyUID: "topology"}
			actions := []runtime.ActionRecord{action}
			tc.change(st, &actions)
			got, err := selectRunTaskResource(st, actions, ref, "run")
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, candidate, *got)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestStandaloneTaskRejectsRunProvenance(t *testing.T) {
	d, req, conn := taskFixture(t)
	d.RunID = "run"
	_, err := d.Start(context.Background(), testRef(req), req)
	require.Error(t, err)
	require.Zero(t, conn.starts)
	d.RunID = ""
	record, err := d.Start(context.Background(), testRef(req), req)
	require.NoError(t, err)
	record.RunID = "run"
	require.NoError(t, d.save(record))
	_, err = d.Observe(context.Background(), testRef(req), record.Handle)
	require.Error(t, err)
}
