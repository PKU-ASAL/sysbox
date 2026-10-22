package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func resetSSHFixture(t *testing.T) (*Executor, ActionRecord, *runtimeSSHConnection) {
	t.Helper()
	ctx, pc, n, conn := runtimeSSHFixture(t)
	old, err := (SSHAccessResourceHandler{}).Create(ctx, pc, n)
	require.NoError(t, err)
	pc.exec.state.AddResource(old)
	conn.calls = nil
	saved, err := decodeSSHState(old, pc.exec.state)
	require.NoError(t, err)
	node := pc.exec.state.Resources[0]
	for _, r := range pc.exec.state.Resources {
		if r.Address.Type == "sysbox_node" {
			node = r
		}
	}
	record := ActionRecord{ID: "reset", RunID: "run", Topology: pc.exec.topology, Resource: node.Address, Action: controlplane.PlanActionReset, OwnerUID: node.OwnerUID, ResourceUID: node.ResourceUID, Generation: node.Generation + 1, Candidate: &node, Status: ActionUncertain, Phase: "verifying", EffectComplete: true, Revision: 1}
	require.Equal(t, node.Address.String(), saved.Node)
	require.NoError(t, pc.exec.pinResetSSH(&record))
	replacement := cloneResetStateResource(node)
	replacement.Generation++
	record.Candidate = &replacement
	stored, err := cloneExecutionState(pc.exec.state)
	require.NoError(t, err)
	pc.exec.actions = &memoryActions{state: stored, actions: []ActionRecord{record}}
	conn.found.Status, conn.found.Current, conn.found.Config = "absent", nil, nil
	return pc.exec, record, conn
}

func TestResetSSHRebuildBindsReplacementAndPreservesOldUntilCommit(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	old := record.ResetSSH[0].Previous
	require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
	require.Len(t, conn.calls, 1)
	dep := record.ResetSSH[0]
	require.True(t, dep.Started)
	require.NotNil(t, dep.Result)
	require.Equal(t, old.Generation+1, dep.Result.Generation)
	saved, err := decodeSSHState(*dep.Result, e.state)
	require.NoError(t, err)
	require.Equal(t, record.Generation, saved.Handle.Generation)
	require.Equal(t, dep.ActionID, saved.Handle.ID)
	require.Equal(t, old.Generation, e.state.FindResource(old.Address).Generation)
	require.NoError(t, record.ValidateResetSSH(e.state))
}

func TestResetSSHColdRecoveryObservesLostReplyWithoutReplay(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	conn.lost = true
	require.ErrorContains(t, e.reconcileResetSSH(context.Background(), &record), "reply lost")
	require.Len(t, conn.calls, 1)
	require.True(t, record.ResetSSH[0].Started)
	require.Nil(t, record.ResetSSH[0].Result)
	cold := NewExecutor(e.graph, e.state)
	cold.SetRunContext(e.topology, "continuation")
	cold.actions = e.actions
	require.NoError(t, cold.reconcileResetSSH(context.Background(), &record))
	require.Len(t, conn.calls, 1)
	require.NotNil(t, record.ResetSSH[0].Result)
}

func TestResetSSHUnknownAdmissionDoesNotReplay(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	record.ResetSSH[0].Started = true
	require.ErrorIs(t, e.reconcileResetSSH(context.Background(), &record), ErrActionUncertain)
	require.Empty(t, conn.calls)
}

func TestResetSSHObservationCannotStartUnadmittedMutation(t *testing.T) {
	e, record, conn := resetSSHFixture(t)
	require.ErrorIs(t, e.reconcileResetSSHWithEffects(context.Background(), &record, false), ErrActionUncertain)
	require.False(t, record.ResetSSH[0].Started)
	require.Empty(t, conn.calls)
}

func TestResetSSHDeleteUsesCurrentGenerationAfterAtomicReset(t *testing.T) {
	e, record, _ := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
	result := *record.ResetSSH[0].Result
	e.state.RemoveResource(result.Address)
	e.state.AddResource(result)
	e.actions.(*memoryActions).actions = append(e.actions.(*memoryActions).actions, ActionRecord{ID: "original-ssh-create", Resource: result.Address, ResourceUID: result.ResourceUID, Generation: result.Generation - 1, Status: ActionSucceeded})
	deletion, err := e.newAction(context.Background(), controlplane.PlannedChange{Address: result.Address, Action: controlplane.PlanActionDelete})
	require.NoError(t, err)
	require.Equal(t, result.Generation, deletion.Generation)
}

func TestResetSSHRejectsLostAdmissionAndForeignResult(t *testing.T) {
	for _, scenario := range []string{"storage", "foreign", "missing", "target"} {
		t.Run(scenario, func(t *testing.T) {
			e, record, conn := resetSSHFixture(t)
			switch scenario {
			case "storage":
				e.actions.(*memoryActions).outcomeErr = errors.New("disk full")
			case "foreign":
				require.NoError(t, e.reconcileResetSSH(context.Background(), &record))
				record.ResetSSH[0].Result = nil
				conn.found.Current.Handle.ID = "foreign"
				conn.calls = nil
			case "missing":
				record.ResetSSH[0].DesiredDigest = "changed"
			case "target":
				record.Candidate = &state.Resource{}
			}
			require.Error(t, e.reconcileResetSSH(context.Background(), &record))
			require.Empty(t, conn.calls)
		})
	}
}

func TestResetSSHDispositionRequiresObservedCommittedDependencies(t *testing.T) {
	e, a, _ := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &a))
	source := e.state.FindResource(a.Resource)
	source.ExternalID = "old-provider"
	previous := &a.ResetSSH[0].Previous
	saved, err := decodeSSHState(*previous, e.state)
	require.NoError(t, err)
	saved.Handle.NodeID = source.ExternalID
	previous.Attributes = state.MustAttributes(map[string]any{"ssh_access": saved})
	old := controlplane.RecoveredResource{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: source.Generation, ExternalID: source.ExternalID, Status: "absent", JournalStatus: string(a.Status), ProviderScope: "local-only", DeleteResidueAbsent: true}
	o := controlplane.RecoveredResource{ActionID: a.ID, Resource: a.Resource.String(), OwnerUID: a.OwnerUID, ResourceUID: a.ResourceUID, Generation: a.Generation, ExternalID: a.Candidate.ExternalID, Status: "present", JournalStatus: string(a.Status), ProviderScope: "local-only", ResetSource: &old}
	dep := a.ResetSSH[0]
	r := dep.Result
	child := controlplane.RecoveredResource{ActionID: dep.ActionID, Resource: r.Address.String(), OwnerUID: r.OwnerUID, ResourceUID: r.ResourceUID, Generation: r.Generation, ExternalID: r.ExternalID, Status: "present", JournalStatus: string(a.Status)}
	o.ResetSSH = []controlplane.RecoveredResource{child}
	p := PlanIntentDisposition(a, []controlplane.RecoveredResource{o})
	require.Equal(t, "retain-managed", p.ResourceDecision)
	require.Equal(t, []string{"effect_stop_proof_required"}, p.Blockers)
	for _, variant := range []string{"missing", "unknown", "old-source", "foreign", "duplicate", "no-result", "previous-attachment", "previous-obligation", "result-obligation"} {
		t.Run(variant, func(t *testing.T) {
			aa := a
			aa.ResetSSH = append([]ResetSSHDependency(nil), a.ResetSSH...)
			obs := o
			obs.ResetSSH = append([]controlplane.RecoveredResource(nil), o.ResetSSH...)
			oldCopy := old
			obs.ResetSource = &oldCopy
			switch variant {
			case "missing":
				obs.ResetSSH = nil
			case "unknown":
				obs.ResetSSH[0].Status = "unknown"
			case "old-source":
				obs.ResetSource.Status = "present"
			case "foreign":
				obs.ResetSSH[0].ResourceUID = "foreign"
			case "duplicate":
				obs.ResetSSH = append(obs.ResetSSH, child)
			case "no-result":
				aa.ResetSSH[0].Result = nil
			case "previous-attachment":
				aa.ResetSSH[0].Previous.Attachments = []state.Attachment{{}}
			case "previous-obligation":
				aa.ResetSSH[0].Previous = cloneResetStateResource(aa.ResetSSH[0].Previous)
				require.NoError(t, aa.ResetSSH[0].Previous.RequireRecovery("older", aa.ResetSSH[0].Previous.ResourceUID, aa.ResetSSH[0].Previous.Generation))
			case "result-obligation":
				r := cloneResetStateResource(*aa.ResetSSH[0].Result)
				require.NoError(t, r.RequireRecovery("older", r.ResourceUID, r.Generation))
				aa.ResetSSH[0].Result = &r
			}
			require.NotEqual(t, "retain-managed", PlanIntentDisposition(aa, []controlplane.RecoveredResource{obs}).ResourceDecision)
		})
	}
}

type resetSSHDispositionDriver struct {
	*nodeGuardDriver
	oldID    string
	residual []string
}

func (d *resetSSHDispositionDriver) HostFenceScope(context.Context) (string, error) {
	return "local-only", nil
}
func (d *resetSSHDispositionDriver) HostFenceDeletionAbsent(_ context.Context, h substrate.NodeHandle) error {
	d.residual = append(d.residual, h.ID)
	if h.ID != d.oldID {
		return errors.New("unexpected source")
	}
	return nil
}
func TestResetSSHDispositionObserverUsesReplacementForChildReads(t *testing.T) {
	e, a, conn := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &a))
	source := e.state.FindResource(a.Resource)
	source.ExternalID = "old-provider"
	previous := &a.ResetSSH[0].Previous
	saved, err := decodeSSHState(*previous, e.state)
	require.NoError(t, err)
	saved.Handle.NodeID = source.ExternalID
	previous.Attributes = state.MustAttributes(map[string]any{"ssh_access": saved})
	*e.state.FindResource(previous.Address) = *previous
	base, err := driver.DefaultRegistry.RequireNode(a.Candidate.Driver)
	require.NoError(t, err)
	wrapper := &resetSSHDispositionDriver{nodeGuardDriver: base.(*nodeGuardDriver), oldID: source.ExternalID}
	wrapper.resetDeleted = map[string]bool{source.ExternalID: true}
	prior := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = prior })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: wrapper.name, Version: "test", Node: wrapper, NIC: wrapper, NodeState: wrapper, GuestNetwork: wrapper, GuestNetworkInit: wrapper, ImageEntry: wrapper}))
	// Provider state is reconstructed by actual readers, not a synthetic report.
	require.NoError(t, source.SetProviderState(json.RawMessage(`{}`)))
	require.NoError(t, a.Candidate.SetProviderState(json.RawMessage(`{}`)))
	repo := &memoryActions{state: e.state, actions: []ActionRecord{a}}
	before, _ := json.Marshal(e.state)
	_, items, err := ObserveRunActions(context.Background(), repo, "lab", a.RunID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].ResetSource)
	require.Equal(t, "absent", items[0].ResetSource.Status)
	require.True(t, items[0].ResetSource.DeleteResidueAbsent)
	require.Len(t, items[0].ResetSSH, 1)
	require.Equal(t, "present", items[0].ResetSSH[0].Status)
	require.Equal(t, "retain-managed", PlanIntentDisposition(a, items).ResourceDecision)
	after, _ := json.Marshal(e.state)
	require.JSONEq(t, string(before), string(after))
	require.Equal(t, []string{source.ExternalID}, wrapper.residual)
	require.Len(t, conn.calls, 1, "read-only observation cannot replay SSH mutation")
}

func TestResetSSHDispositionRecoversLostResultWithoutChangingJournal(t *testing.T) {
	e, a, _ := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &a))
	dep := &a.ResetSSH[0]
	saved, err := decodeSSHState(*dep.Result, e.state)
	require.NoError(t, err)
	committed := substrate.SSHAccessResult{Handle: saved.Handle, Present: true}
	dep.Result = nil
	before, _ := json.Marshal(a)
	child := controlplane.RecoveredResource{ActionID: dep.ActionID, Resource: dep.Previous.Address.String(), OwnerUID: a.OwnerUID, ResourceUID: dep.Previous.ResourceUID, Generation: dep.Previous.Generation + 1, ExternalID: dep.Config.AccessID, Status: "present", JournalStatus: string(a.Status), SSHCommitted: &committed}
	observation := controlplane.RecoveredResource{ActionID: a.ID, ResetSSH: []controlplane.RecoveredResource{child}}
	for _, variant := range []string{"exact", "missing", "foreign-action", "foreign-node", "digest", "not-present", "duplicate", "not-started", "changed-config"} {
		t.Run(variant, func(t *testing.T) {
			aa := a
			aa.ResetSSH = append([]ResetSSHDependency(nil), a.ResetSSH...)
			o := observation
			o.ResetSSH = append([]controlplane.RecoveredResource(nil), observation.ResetSSH...)
			r := committed
			o.ResetSSH[0].SSHCommitted = &r
			switch variant {
			case "missing":
				o.ResetSSH[0].SSHCommitted = nil
			case "foreign-action":
				r.Handle.ID = "foreign"
			case "foreign-node":
				r.Handle.NodeID = "foreign"
			case "digest":
				r.Handle.Digest = "foreign"
			case "not-present":
				r.Present = false
			case "duplicate":
				o.ResetSSH = append(o.ResetSSH, child)
			case "not-started":
				aa.ResetSSH[0].Started = false
			case "changed-config":
				aa.ResetSSH[0].Config.Port++
			}
			resolved, err := ResetSSHWithObservedResults(aa, []controlplane.RecoveredResource{o})
			if variant != "exact" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, resolved.ResetSSH[0].Result)
			require.True(t, resolved.ResetSSHResultsRecorded())
			require.NoError(t, resolved.ValidateResetSSH(e.state))
		})
	}
	after, _ := json.Marshal(a)
	require.JSONEq(t, string(before), string(after))
}

func TestResetSSHDispositionObserverDiscoversLostCommittedResult(t *testing.T) {
	e, a, conn := resetSSHFixture(t)
	require.NoError(t, e.reconcileResetSSH(context.Background(), &a))
	source := e.state.FindResource(a.Resource)
	source.ExternalID = "old-provider"
	previous := &a.ResetSSH[0].Previous
	saved, err := decodeSSHState(*previous, e.state)
	require.NoError(t, err)
	saved.Handle.NodeID = source.ExternalID
	previous.Attributes = state.MustAttributes(map[string]any{"ssh_access": saved})
	*e.state.FindResource(previous.Address) = *previous
	base, err := driver.DefaultRegistry.RequireNode(a.Candidate.Driver)
	require.NoError(t, err)
	wrapper := &resetSSHDispositionDriver{nodeGuardDriver: base.(*nodeGuardDriver), oldID: source.ExternalID}
	wrapper.resetDeleted = map[string]bool{source.ExternalID: true}
	prior := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = prior })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: wrapper.name, Version: "test", Node: wrapper, NIC: wrapper, NodeState: wrapper, GuestNetwork: wrapper, GuestNetworkInit: wrapper, ImageEntry: wrapper}))
	// Provider state is reconstructed by actual readers, not a synthetic report.
	require.NoError(t, source.SetProviderState(json.RawMessage(`{}`)))
	require.NoError(t, a.Candidate.SetProviderState(json.RawMessage(`{}`)))
	a.ResetSSH[0].Result = nil
	repo := &memoryActions{state: e.state, actions: []ActionRecord{a}}
	before, _ := json.Marshal(e.state)
	_, items, err := ObserveRunActions(context.Background(), repo, "lab", a.RunID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].ResetSource)
	require.Equal(t, "absent", items[0].ResetSource.Status)
	require.True(t, items[0].ResetSource.DeleteResidueAbsent)
	require.Len(t, items[0].ResetSSH, 1)
	require.NotNil(t, items[0].ResetSSH[0].SSHCommitted)
	require.Nil(t, repo.actions[0].ResetSSH[0].Result)
	require.Equal(t, "present", items[0].ResetSSH[0].Status)
	require.Equal(t, "retain-managed", PlanIntentDisposition(a, items).ResourceDecision)
	after, _ := json.Marshal(e.state)
	require.JSONEq(t, string(before), string(after))
	require.Equal(t, []string{source.ExternalID}, wrapper.residual)
	require.Len(t, conn.calls, 1, "read-only observation cannot replay SSH mutation")
}
