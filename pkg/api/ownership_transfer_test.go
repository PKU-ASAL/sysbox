package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipTransferPreparationRetiresSourceAndReservesTarget(t *testing.T) {
	testOwnershipTransferFlow(t, "")
}
func TestOwnershipTransferProcessCrashMatrix(t *testing.T) {
	for _, phase := range []string{"prepared", "retired", "target-committed", "operation-committed", "stop-retried", "barrier-released", "completion-response"} {
		t.Run(phase, func(t *testing.T) { testOwnershipTransferFlow(t, phase) })
	}
}
func testOwnershipTransferFlow(t *testing.T, phase string) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		sourceRepo, err := s.actionRepository("source")
		require.NoError(t, err)
		targetRepo, err := s.actionRepository("target")
		require.NoError(t, err)
		manager := state.NewManagerWithBackend(sourceRepo.StateBackend("source"))
		source, err := manager.Load()
		require.NoError(t, err)
		target, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		old := substrate.OperationIdentity{ActionID: "old", OwnerUID: source.TopologyUID, TopologyUID: source.TopologyUID, ResourceUID: "old-resource", Generation: 1}
		next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: target.TopologyUID, TopologyUID: target.TopologyUID, ResourceUID: "new-resource", Generation: 2}
		source.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "old"), Driver: "libvirt", ResourceType: "sysbox_node", ExternalID: "external", OwnerUID: old.OwnerUID, ResourceUID: old.ResourceUID, Generation: 1}}
		require.NoError(t, manager.Save(source))
		source, err = manager.Load()
		require.NoError(t, err)
		plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: old, Target: next, SourceSerial: source.Meta.Serial, TargetSerial: target.Meta.Serial, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
		stalePlan := plan
		stalePlan.ID = "stale-transfer"
		stalePlan.Target.ActionID = stalePlan.ID
		stalePlan.SourceSerial++
		_, err = s.prepareOwnershipTransfer(ctx, stalePlan)
		require.Error(t, err)
		_, err = targetRepo.ReadOwnershipTarget(ctx, "target", stalePlan.ID)
		require.ErrorIs(t, err, persistence.ErrNotFound)
		cancelled := plan
		cancelled.ID = "cancelled"
		cancelled.Target.ActionID = cancelled.ID
		require.NoError(t, targetRepo.ReserveOwnershipTarget(ctx, cancelled))
		require.NoError(t, s.abortOwnershipTransfer(ctx, cancelled))
		require.Error(t, targetRepo.ReserveOwnershipTarget(ctx, cancelled))
		_, err = s.beginOwnershipMaintenance(ctx, cancelled)
		require.Error(t, err, "cancelled target cannot create a delayed source barrier")
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "host", "worker", "")
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: source.TopologyUID}, AgentID: "host", SessionID: session.ID, CommandID: "old", ActionID: "old", Attempt: 1, Operation: "run_apply", Digest: "old", TargetBootID: "boot"}, time.Minute)
		require.NoError(t, err)
		_, err = s.prepareOwnershipTransfer(ctx, plan)
		require.Error(t, err)
		require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified"))
		rawPlan, err := json.Marshal(plan)
		require.NoError(t, err)
		unpreparedRequest := httptest.NewRequest("POST", "/v1/topologies/target/ownership-transfers", bytes.NewReader(rawPlan))
		unpreparedRequest.Header.Set("X-Sysbox-Roles", "admin")
		unprepared := httptest.NewRecorder()
		s.ServeHTTP(unprepared, unpreparedRequest)
		require.Equal(t, 409, unprepared.Code)
		var completion *HostNodeOperationCompletion
		var completionKey ed25519.PublicKey
		crash := func(stage string, op controlplane.NodeOperation, evidence json.RawMessage) {
			if phase != stage {
				return
			}
			input := ownershipCrashInput{Phase: stage, Plan: plan, Issuer: key, Operation: op, Evidence: evidence, Completion: completion, HostKey: completionKey, Ready: filepath.Join(t.TempDir(), "ready")}
			switch typed := store.(type) {
			case *localAPIStore:
				input.Kind = "file"
				input.Path = typed.runsDir
			case *sqliteAPIStore:
				input.Kind = "sqlite"
				input.Path = typed.dbPath
			case *postgresAPIStore:
				input.Kind = "postgres"
				input.Path = typed.dsn
			}
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "input.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			child := exec.Command(os.Args[0], "-test.run=^TestOwnershipTransferCrashChild$")
			child.Env = append(os.Environ(), "SYSBOX_TRANSFER_CRASH_CHILD="+path)
			log, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
			require.NoError(t, err)
			defer log.Close()
			child.Stdout, child.Stderr = log, log
			require.NoError(t, child.Start())
			defer child.Process.Kill()
			require.Eventually(t, func() bool { _, err := os.Stat(input.Ready); return err == nil }, 20*time.Second, 20*time.Millisecond, "child log: %s", log.Name())
			require.NoError(t, child.Process.Kill())
			var exited *exec.ExitError
			require.ErrorAs(t, child.Wait(), &exited)
			require.Equal(t, -1, exited.ExitCode(), "requires OS SIGKILL")
		}
		crash("prepared", controlplane.NodeOperation{}, nil)
		crash("retired", controlplane.NodeOperation{}, nil)
		proof, err := s.prepareOwnershipTransfer(ctx, plan)
		require.NoError(t, err)
		require.NoError(t, controlplane.VerifyOwnershipRetirement(pub, proof))
		require.Error(t, s.abortOwnershipTransfer(ctx, plan), "retired source cannot be silently restored by abort")
		cold := stateDeleteTestServer(t, store)
		require.NoError(t, cold.ConfigureExecutionGrantSigner(key))
		again, err := cold.prepareOwnershipTransfer(ctx, plan)
		require.NoError(t, err)
		require.Equal(t, proof.Signature, again.Signature)
		after, err := sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		require.Empty(t, after.Resources)
		targetManager := state.NewManagerWithBackend(targetRepo.StateBackend("target"))
		require.Error(t, targetManager.Save(target))
		require.Error(t, authority.Authorize(ctx, *grant))
		host, hostKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
		req := controlplane.AdoptionRequest{Transfer: &proof, ID: plan.ID, Topology: plan.TargetTopology, TopologyUID: next.TopologyUID, Address: plan.TargetAddress, Provider: plan.Provider, AgentID: plan.AgentID, ExternalID: plan.ExternalID, ResourceUID: next.ResourceUID, Generation: next.Generation, ExpectedSerial: plan.TargetSerial, Evidence: substrate.AdoptionEvidence{Provider: plan.Provider, ExternalID: plan.ExternalID, Fingerprint: plan.Fingerprint}}
		op := prepareNodeOperation(controlplane.NodeOperation{ID: plan.ID, Topology: plan.TargetTopology, Node: "new", AgentID: plan.AgentID, Operation: "adoption_apply", ResourceUID: next.ResourceUID, Generation: next.Generation, Adoption: &req})
		op, err = (&NodeOperationService{operations: newNodeOperationStore(store)}).enqueue(ctx, target.TopologyUID, op)
		require.NoError(t, err)
		cmd, ok, err := store.AcquireAgentCommandLease(ctx, "host", op.CommandID, "transfer-worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		transferGrant, err := s.issueCommandGrant(ctx, "host", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"})
		require.NoError(t, err)
		evidence := json.RawMessage(`{"domain_uuid":"external"}`)
		crash("target-committed", op, evidence)
		receipt, err := controlplane.SignAdoptionStopReceipt(hostKey, *transferGrant, "boot", "transfer-receipt", evidence, time.Now())
		require.NoError(t, err)
		completion = &HostNodeOperationCompletion{Receipt: receipt, Evidence: evidence}
		completionKey = host
		for _, boundary := range []string{"operation-committed", "stop-retried", "barrier-released", "completion-response"} {
			crash(boundary, op, evidence)
		}
		if phase == "target-committed" || phase == "operation-committed" || phase == "stop-retried" || phase == "barrier-released" || phase == "completion-response" {
			committed, err := targetRepo.LoadState(ctx, "target")
			require.NoError(t, err)
			require.Equal(t, target.Meta.Serial+1, committed.Meta.Serial)
		}
		if phase == "operation-committed" || phase == "stop-retried" || phase == "barrier-released" || phase == "completion-response" {
			currentOp, err := store.GetNodeOperation(ctx, plan.ID)
			require.NoError(t, err)
			require.Equal(t, controlplane.NodeOperationStatusDone, currentOp.Status)
			currentGrant, err := authority.InspectGrant(ctx, "host", transferGrant.Scope)
			require.NoError(t, err)
			// CompleteAdoptionFromHost already stores the stop receipt
			// (in the same SQL transaction as operation completion). The
			// handler's later AcceptHostStop is an idempotent retry.
			require.Equal(t, controlplane.GrantStopped, currentGrant.State)
			require.NotEmpty(t, currentGrant.StopReceipt)
			currentBarrier, err := authority.InspectMaintenance(ctx, plan.ID)
			require.NoError(t, err)
			require.Equal(t, phase == "barrier-released" || phase == "completion-response", currentBarrier.Released)
		}
		// A fresh API instance handles the retry after the child API died.
		s = stateDeleteTestServer(t, store)
		require.NoError(t, s.ConfigureExecutionGrantSigner(key))
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
		raw, err := json.Marshal(*completion)
		require.NoError(t, err)
		for range 2 {
			response := httptest.NewRecorder()
			s.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/host/node-operations/"+plan.ID+"/completed", bytes.NewReader(raw)))
			require.Equal(t, 200, response.Code, response.Body.String())
		}
		afterTarget, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		require.Len(t, afterTarget.Resources, 1)
		require.Equal(t, target.Meta.Serial+1, afterTarget.Meta.Serial)
		barrier, err := authority.InspectMaintenance(ctx, plan.ID)
		require.NoError(t, err)
		require.True(t, barrier.Released)
		require.Error(t, authority.Authorize(ctx, *transferGrant))
		planRaw, err := json.Marshal(plan)
		require.NoError(t, err)
		repeated := httptest.NewRecorder()
		replayRequest := httptest.NewRequest("POST", "/v1/topologies/target/ownership-transfers", bytes.NewReader(planRaw))
		replayRequest.Header.Set("X-Sysbox-Roles", "admin")
		s.ServeHTTP(repeated, replayRequest)
		require.Equal(t, 202, repeated.Code, repeated.Body.String())

	})
}

func TestOwnershipAbortCannotReleaseForeignMaintenanceID(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		repo, err := s.actionRepository("target")
		require.NoError(t, err)
		st, err := repo.LoadState(ctx, "target")
		require.NoError(t, err)
		source, err := s.actionRepository("source")
		require.NoError(t, err)
		old, err := source.LoadState(ctx, "source")
		require.NoError(t, err)
		p := controlplane.OwnershipTransferPlan{Version: 1, ID: "same-id", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "old", OwnerUID: old.TopologyUID, TopologyUID: old.TopologyUID, ResourceUID: "old", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "same-id", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "new", Generation: 2}, TargetSerial: st.Meta.Serial, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
		require.NoError(t, repo.ReserveOwnershipTarget(ctx, p))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		_, err = authority.BeginMaintenance(ctx, p.ID, "another-topology", "unrelated")
		require.NoError(t, err)
		require.Error(t, s.abortOwnershipTransfer(ctx, p))
		barrier, err := authority.InspectMaintenance(ctx, p.ID)
		require.NoError(t, err)
		require.False(t, barrier.Released)
	})
}

type ownershipCrashInput struct {
	Phase, Kind, Path, Ready string
	Completion               *HostNodeOperationCompletion
	HostKey                  ed25519.PublicKey
	Plan                     controlplane.OwnershipTransferPlan
	Issuer                   ed25519.PrivateKey
	Operation                controlplane.NodeOperation
	Evidence                 json.RawMessage
}

func TestOwnershipTransferCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_TRANSFER_CRASH_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input ownershipCrashInput
	require.NoError(t, json.Unmarshal(raw, &input))
	var store apiStore
	switch input.Kind {
	case "file":
		store = &localAPIStore{runsDir: input.Path}
	case "sqlite":
		store = &sqliteAPIStore{dbPath: input.Path, runsDir: t.TempDir()}
	case "postgres":
		store = &postgresAPIStore{dsn: input.Path}
	default:
		t.Fatal("invalid backend")
	}
	ctx := context.Background()
	s := stateDeleteTestServer(t, store)
	require.NoError(t, s.ConfigureExecutionGrantSigner(input.Issuer))
	switch input.Phase {
	case "prepared":
		target, err := s.actionRepository(input.Plan.TargetTopology)
		require.NoError(t, err)
		require.NoError(t, target.ReserveOwnershipTarget(ctx, input.Plan))
		_, err = s.beginOwnershipMaintenance(ctx, input.Plan)
		require.NoError(t, err)
	case "retired":
		_, err = s.prepareOwnershipTransfer(ctx, input.Plan)
		require.NoError(t, err)
	case "target-committed":
		require.NoError(t, s.commitAdoptionState(ctx, input.Operation, input.Evidence))
	case "operation-committed", "stop-retried", "barrier-released":
		// Execute the production workflow's durable boundaries, then kill the
		// process. The retry below enters the public completion handler.
		require.NotNil(t, input.Completion)
		require.NoError(t, s.commitAdoptionState(ctx, input.Operation, input.Evidence))
		_, err := store.CompleteAdoptionFromHost(ctx, "host", input.Plan.ID, input.Completion.Receipt)
		require.NoError(t, err)
		if input.Phase != "operation-committed" {
			authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
			raw, err := json.Marshal(input.Completion.Receipt)
			require.NoError(t, err)
			sum := sha256.Sum256(raw)
			require.NoError(t, authority.AcceptHostStop(ctx, input.Completion.Receipt.Grant, hex.EncodeToString(sum[:])))
			if input.Phase == "barrier-released" {
				barrier, err := authority.InspectMaintenance(ctx, input.Plan.ID)
				require.NoError(t, err)
				require.NoError(t, authority.EndMaintenance(ctx, *barrier))
			}
		}
	case "completion-response":
		require.NotNil(t, input.Completion)
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": input.HostKey}
		raw, err := json.Marshal(input.Completion)
		require.NoError(t, err)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, httptest.NewRequest("POST", "/v1/hosts/host/node-operations/"+input.Plan.ID+"/completed", bytes.NewReader(raw)))
		require.Equal(t, 200, response.Code, response.Body.String())
	default:
		t.Fatal("invalid crash phase")
	}
	require.NoError(t, os.WriteFile(input.Ready, []byte(input.Phase), 0600))
	select {}
}
