package runtime

import (
	"context"
	"errors"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOwningAgentObserveAndRecoverLostCommit(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	repo := e.actions.(*memoryActions)
	repo.patchErr = errors.New("commit lost")
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, controlplane.PlanActionCreate)))
	require.Equal(t, 1, h.creates)
	repo.patchErr = nil
	_, observed, err := ObserveRunActions(context.Background(), repo, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.Equal(t, "present", observed[0].Status)
	require.Equal(t, repo.actions[0].ResourceUID, observed[0].ResourceUID)
	require.NoError(t, e.RecoverRunActions(context.Background(), "run-1"))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
	require.Equal(t, ActionSucceeded, repo.actions[0].Status)
}
func TestOwningAgentRecoveryUnknownCannotReplay(t *testing.T) {
	e, h, a := convergenceFixture(t, state.ResourceAbsent, false)
	h.afterCreate = state.ResourceUnknown
	require.Error(t, e.Apply(context.Background(), singleConvergencePlan(a, controlplane.PlanActionCreate)))
	_, observed, err := ObserveRunActions(context.Background(), e.actions, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.Equal(t, "unknown", observed[0].Status)
	require.Error(t, e.RecoverRunActions(context.Background(), "run-1"))
	require.Equal(t, 1, h.creates)
	require.Zero(t, h.deletes)
	require.Error(t, e.RecoverRunActions(context.Background(), "foreign-run"))
}

func TestResetObservationAccountsForSourceAndReplacementGenerations(t *testing.T) {
	e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
	repo := e.actions.(*memoryActions)
	source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
	replacement := source
	replacement.ExternalID = "new"
	replacement.Generation = 2
	repo.actions = []ActionRecord{{ID: "reset", Topology: "test-topology", RunID: "run-1", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Replacement: &replacement, Phase: "applying", ReplayForbidden: true}}
	_, observed, err := ObserveRunActions(context.Background(), repo, "test-topology", "run-1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.NotNil(t, observed[0].ResetSource)
	require.NotNil(t, observed[0].ResetReplacement)
	require.Equal(t, int64(1), observed[0].ResetSource.Generation)
	require.Equal(t, int64(2), observed[0].ResetReplacement.Generation)
	require.Equal(t, "old", observed[0].ResetSource.ExternalID)
	require.Equal(t, "new", observed[0].ResetReplacement.ExternalID)
	require.Equal(t, int64(2), observed[0].Generation)
	require.Equal(t, "unknown", observed[0].Status, "composite reset success is never inferred")
}

func TestNewResetAfterSourceDispositionUsesNextActualGeneration(t *testing.T) {
	e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
	repo := e.actions.(*memoryActions)
	source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
	e.state.Resources = []state.Resource{source}
	repo.actions = []ActionRecord{{ID: "reset-old", RunID: "old-run", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Phase: "destroying", ReplayForbidden: true, DispositionID: "intent:explicit-source-retention"}}
	next, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: controlplane.PlanActionReset})
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Generation, "the fenced abandoned reset never created generation 2")
	require.Equal(t, int64(1), next.Candidate.Generation)
	repo.actions[0].Phase = "applying"
	next, err = e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: controlplane.PlanActionReset})
	require.NoError(t, err)
	require.Greater(t, next.Generation, int64(2), "unknown replacement must not reuse a generation")
}

func TestReadAndDeleteAfterResetDispositionUseActualSourceGeneration(t *testing.T) {
	for _, kind := range []controlplane.PlanActionType{controlplane.PlanActionRead, controlplane.PlanActionDelete} {
		t.Run(string(kind), func(t *testing.T) {
			e, _, addr := convergenceFixture(t, state.ResourcePresent, false)
			repo := e.actions.(*memoryActions)
			source := state.Resource{Address: addr, Driver: "fixture", ExternalID: "old", OwnerUID: e.state.TopologyUID, ResourceUID: "resource", Generation: 1}
			e.state.Resources = []state.Resource{source}
			repo.actions = []ActionRecord{{ID: "reset-old", RunID: "old-run", Resource: addr, Action: controlplane.PlanActionReset, Status: ActionUncertain, OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 2, Candidate: &source, Phase: "destroying", ReplayForbidden: true, DispositionID: "intent:source-retention"}}
			next, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: addr, Action: kind})
			require.NoError(t, err)
			require.Equal(t, int64(1), next.Generation)
			require.Equal(t, next.Candidate.Generation, next.Generation)
		})
	}
}

func TestMissingCreateIdentityUsesPinnedReadOnlyDiscovery(t *testing.T) {
	e, base, _, addr := nodeGuardExecutor(t, nil)
	n := e.graph.Get(addr)
	digest, err := desiredHash(n)
	require.NoError(t, err)
	baseDriver := base
	wrapper := &missingIdentityDriver{nodeGuardDriver: baseDriver}
	prior := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = prior })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: base.name, Version: "test", Node: wrapper, NodeState: wrapper}))
	e.state.TopologyUID = "owner"
	a := ActionRecord{ID: "create", RunID: "run", Resource: addr, Action: controlplane.PlanActionCreate, Status: ActionUncertain, OwnerUID: "owner", ResourceUID: "node", Generation: 1, DesiredDigest: digest, ReplayForbidden: true}
	repo := &memoryActions{state: e.state, actions: []ActionRecord{a}}
	_, obs, err := ObserveRunActions(context.Background(), repo, "lab", "run", e.graph)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	require.NotNil(t, obs[0].Discovered)
	require.Equal(t, "found", obs[0].Discovered.ExternalID)
	require.Equal(t, "present", obs[0].Status)
	p := PlanIntentDisposition(a, obs)
	require.Equal(t, "retain-managed", p.ResourceDecision)
	require.Contains(t, p.Blockers, "guest_effect_disposition_required")
	for _, mode := range []string{"foreign-owner", "wrong-digest", "missing", "duplicate", "not-present"} {
		t.Run(mode, func(t *testing.T) {
			copies := append([]controlplane.RecoveredResource(nil), obs...)
			candidate := *copies[0].Discovered
			copies[0].Discovered = &candidate
			switch mode {
			case "foreign-owner":
				candidate.OwnerUID = "foreign"
			case "wrong-digest":
				candidate.Attributes = state.MustAttributes(map[string]any{"desired_hash": "wrong"})
			case "missing":
				copies[0].Discovered = nil
			case "duplicate":
				copies = append(copies, copies[0])
			case "not-present":
				copies[0].Status = "unknown"
			}
			_, err := IntentWithDiscoveredIdentity(a, copies)
			require.Error(t, err)
		})
	}
	require.Nil(t, repo.actions[0].Candidate)
	require.Nil(t, e.state.FindResource(addr))
	require.Zero(t, base.creates)
	require.Zero(t, base.starts)
	cfg := n.Data.(*config.NodeConfig)
	cfg.Routes = []config.RouteConfig{{Destination: "10.0.1.0/24", Via: "10.0.0.1"}}
	cfg.Provisioners = []config.ProvisionerConfig{{Type: "file", Source: "source", Destination: "destination"}}
	a.DesiredDigest, err = desiredHash(n)
	require.NoError(t, err)
	repo.actions = []ActionRecord{a}
	_, composite, err := ObserveRunActions(context.Background(), repo, "lab", "run", e.graph)
	require.NoError(t, err)
	require.NotNil(t, composite[0].Discovered, "identity is discoverable even when guest setup outcome is unknown")
	plan := PlanIntentDisposition(a, composite)
	require.Equal(t, "retain-managed", plan.ResourceDecision)
	require.Contains(t, plan.Blockers, "guest_effect_disposition_required")
	_, err = (NodeResourceHandler{}).Discover(substrate.WithOperationIdentity(context.Background(), actionIdentity(a)), &ProviderContext{exec: e}, n)
	require.Error(t, err, "ordinary recovery must not infer completion from this identity")
	require.Nil(t, repo.actions[0].Candidate)
	cfg.Links = []config.LinkConfig{{Name: "eth0", Network: "sysbox_network.lan"}}
	a.DesiredDigest, err = desiredHash(n)
	require.NoError(t, err)
	repo.actions = []ActionRecord{a}
	_, linked, err := ObserveRunActions(context.Background(), repo, "lab", "run", e.graph)
	require.NoError(t, err)
	require.Nil(t, linked[0].Discovered, "missing attachment inventory remains unresolved")
	a.DesiredDigest = "changed"
	repo.actions = []ActionRecord{a}
	_, obs, err = ObserveRunActions(context.Background(), repo, "lab", "run", e.graph)
	require.NoError(t, err)
	require.Nil(t, obs[0].Discovered)
}

type missingIdentityDriver struct{ *nodeGuardDriver }

func (d *missingIdentityDriver) DiscoverManagedNode(_ context.Context, id substrate.OperationIdentity, _ string) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{ID: "found", Generation: id.Generation}, nil
}
func (d *missingIdentityDriver) HostFenceScope(context.Context) (string, error) {
	return "local-only", nil
}
func (d *missingIdentityDriver) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	id, _ := substrate.OperationIdentityFromContext(ctx)
	return substrate.NodeObservation{Exists: true, ExternalID: h.ID, OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Generation: id.Generation, Status: substrate.NodeStatusExited}, nil
}
