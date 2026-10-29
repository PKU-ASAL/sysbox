package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestHostFenceReleaseRequiresIndependentStopsAndCompletedDeletes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := NewFilesystemExecutionGrantRepository(root)
	session, err := repo.OpenSession(ctx, "agent", "process", "")
	require.NoError(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	grant, err := repo.Issue(ctx, controlplane.ExecutionGrant{AgentID: "agent", SessionID: session.ID, CommandID: "original", ActionID: "run", Attempt: 1, Operation: "run_destroy", Digest: strings.Repeat("a", 64), TargetBootID: "11111111-1111-4111-8111-111111111111", Scope: controlplane.ExecutionScope{TopologyUID: "owner"}}, time.Minute)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "run", LabID: "disposable", ControllerKey: pub, JournalSHA256: strings.Repeat("b", 64), Grant: *grant}
	_, err = repo.BeginHostFence(ctx, req)
	require.NoError(t, err)
	digest, err := req.Digest()
	require.NoError(t, err)
	receipt, err := controlplane.SignHostFenceReceipt(key, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: req.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "12", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, repo.RecordHostFence(ctx, req.ID, receipt))
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Op: "destroy", Operation: "destroy", Status: controlplane.RunFailed, Attempt: 1, QueuedAt: time.Now().UTC().Add(-time.Minute), EndedAt: time.Now().UTC(), Revision: "revision"}
	a := newAggregate()
	a.Serial = 1
	a.State.TopologyUID = "owner"
	action := runtime.ActionRecord{ID: "delete", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, OwnerUID: "owner", ResourceUID: "node", Generation: 1, Resource: address.Resource("sysbox_node", "web"), Action: model.PlanActionDelete, Revision: 1, Status: runtime.ActionSucceeded, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "web"), ExternalID: "external", OwnerUID: "owner", ResourceUID: "node", Generation: 1}}
	a.Actions[action.ID] = action
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{CommandID: "observe", RunID: run.ID, AgentID: "agent", RunAttempt: run.Attempt, QueuedAt: run.QueuedAt, Revision: run.Revision, Mode: controlplane.RunRecoveryObserve, TopologyUID: "owner", StateSerial: a.Serial, ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{{ActionID: action.ID, Resource: action.Resource.String(), OwnerUID: "owner", ResourceUID: "node", Generation: 1, ExternalID: "external", Status: "absent", JournalStatus: "succeeded"}}}
	report, err = controlplane.SignFenceObservation(hostKey, report, controlplane.FenceObservationBinding{RequestSHA256: digest, HostBootID: receipt.NewBootID}, "observer", 1)
	require.NoError(t, err)
	cmd := CommandRecord{Retired: true, Command: controlplane.AgentCommand{ID: "observe", AgentID: "agent", Type: controlplane.RunRecoveryCommand, RecoveryMode: controlplane.RunRecoveryObserve, ActionID: req.ID, TopologyUID: "owner", ExpectedSerial: a.Serial, Run: &run, Status: controlplane.AgentCommandStatusCompleted, LeaseOwner: "observer", Attempt: 1}, Recovery: &report}
	release := HostFenceReleaseRequest{FenceID: req.ID, Key: "release-key", ObservationCommandID: cmd.Command.ID}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	authority, err := repo.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	_, _, err = ApplyHostFenceRelease(authority, raw, run, cmd, hostPub, release, time.Now().UTC())
	require.ErrorIs(t, err, ErrConflict, "receipt cannot substitute for independently authenticated stops")
	require.NoError(t, repo.AcceptHostStop(ctx, *grant, "independent-host-stop"))
	authority, err = repo.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	for _, variant := range []string{"uncertain", "running", "unsigned", "tampered", "foreign-key", "present", "wrong-scope", "missing-fence", "residue", "no-candidate", "missing-action"} {
		t.Run(variant, func(t *testing.T) {
			snapshot := *a
			snapshot.Actions = map[string]runtime.ActionRecord{action.ID: action}
			r := run
			c := cmd
			proof := report
			c.Recovery = &proof
			proof.Resources = append([]model.RecoveredResource(nil), report.Resources...)
			public := hostPub
			request := release
			switch variant {
			case "uncertain":
				bad := action
				bad.Status = runtime.ActionUncertain
				snapshot.Actions[action.ID] = bad
			case "running":
				r.Status = controlplane.RunRunning
			case "unsigned":
				proof.HostObservation = nil
			case "tampered":
				proof.StateSerial++
			case "foreign-key":
				public = pub
			case "present":
				proof.Resources[0].Status = "present"
			case "wrong-scope":
				stateCopy := state.State{Version: a.State.Version, TopologyUID: a.State.TopologyUID}
				snapshot.State = &stateCopy
				snapshot.State.TopologyUID = "other"
			case "residue":
				stateCopy := state.State{Version: a.State.Version, TopologyUID: a.State.TopologyUID}
				stateCopy.Resources = []state.Resource{*action.Candidate}
				snapshot.State = &stateCopy
			case "no-candidate":
				altered := action
				altered.Candidate = nil
				snapshot.Actions[action.ID] = altered
			case "missing-action":
				snapshot.Actions = map[string]runtime.ActionRecord{}
			case "missing-fence":
				request.FenceID = "missing"
			}
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			next, _, err := ApplyHostFenceRelease(authority, encoded, r, c, public, request, time.Now().UTC())
			require.Error(t, err)
			require.Nil(t, next)
		})
	}
	next, result, err := ApplyHostFenceRelease(authority, raw, run, cmd, hostPub, release, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "release-key", result.Key)
	lifecycleRepo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, lifecycleRepo.update(ctx, "lab", func(current *aggregate) error { *current = *a; return nil }))
	checked := false
	lifecycleRepo.backend = &releasePublicationBackend{backend: lifecycleRepo.backend, check: func() {
		checked = true
		held, err := repo.ReadHostFence(ctx, req.ID)
		require.NoError(t, err)
		require.NotNil(t, held.Release, "authority must already be published before releasing lifecycle exclusion")
	}}
	result, err = lifecycleRepo.CommitFilesystemHostFenceRelease(ctx, root, "lab", release, hostPub, func() (controlplane.Run, CommandRecord, error) { return run, cmd, nil })
	require.NoError(t, err)
	require.True(t, checked)
	next, err = repo.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	current, err := repo.ReadHostFence(ctx, req.ID)
	require.NoError(t, err)
	require.NotNil(t, current.Release)
	// Cold validation rejects released barriers without both proof and stop history.
	var ledger executionAuthority
	require.NoError(t, json.Unmarshal(next, &ledger))
	delete(ledger.Stops, grant.Epoch)
	corrupt, err := json.Marshal(ledger)
	require.NoError(t, err)
	_, err = decodeExecutionAuthority(corrupt)
	require.ErrorIs(t, err, ErrCorruptRecord)

	barrier, err := repo.InspectMaintenance(ctx, current.BarrierID)
	require.NoError(t, err)
	require.True(t, barrier.Released)
	// Exact replay ignores changed work after release and cannot release a successor.
	replay, got, err := ApplyHostFenceRelease(next, nil, controlplane.Run{}, CommandRecord{}, nil, release, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, result, got)
	require.Equal(t, next, replay)
	conflict := release
	conflict.Key = "other"
	_, _, err = ApplyHostFenceRelease(next, raw, run, cmd, hostPub, conflict, time.Now().UTC())
	require.ErrorIs(t, err, ErrConflict)
	other := *grant
	other.Epoch = 0
	other.ActionID = "next"
	other.CommandID = "next"
	other.Scope.TopologyUID = "other"
	_, err = repo.Issue(ctx, other, time.Minute)
	require.NoError(t, err)
}

// Inspect immediately after the callback while the underlying lifecycle lock
// remains held. This deterministically detects publication after lock release.
type releasePublicationBackend struct {
	backend
	check func()
}

func (b *releasePublicationBackend) update(ctx context.Context, key string, fn func(*aggregate) error) error {
	return b.backend.update(ctx, key, func(a *aggregate) error {
		err := fn(a)
		if err == nil {
			b.check()
		}
		return err
	})
}
