package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestGCFenceDispositionClosesUnknownWithoutDeletingAgain(t *testing.T) {
	for _, stage := range []string{"none", "prepared", "command-retired", "reservation-committed"} {
		t.Run(stage, func(t *testing.T) { runGCFenceDisposition(t, stage) })
	}
}
func runGCFenceDisposition(t *testing.T, stage string) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		secret := "gc-fence-agent-secret"
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "host-a", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
		host, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"host-a": host}
		controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.fenceTargets = map[string]hostFenceTarget{"host-a": {LabID: "isolated", PublicKey: controllerPub}}
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		refs, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		c := controlplane.GCCandidate{Kind: "domain", ExternalID: "external", TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
		reservation := persistence.GCReservation{ID: "gc-fence-test", AgentID: "host-a", Candidate: c}
		require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", reservation))
		require.NoError(t, s.publishGCReservation(ctx, "lab", reservation))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "host-a", "process", "")
		require.NoError(t, err)
		cmd, ok, err := store.AcquireAgentCommandLease(ctx, "host-a", reservation.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		grant, err := s.issueCommandGrant(ctx, "host-a", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "11111111-1111-4111-8111-111111111111"})
		require.NoError(t, err)
		request := controlplane.HostFenceRequest{Version: 1, ID: "gcf-test", Topology: "lab", RunID: reservation.ID, LabID: "isolated", ControllerKey: controllerPub, JournalSHA256: controlstore.GcEvidenceHash(reservation), Grant: *grant}
		beginBody, _ := json.Marshal(map[string]any{"grant": grant})
		begin := httptest.NewRequest("POST", "/v1/topologies/lab/gc/"+reservation.ID+"/host-fences", bytes.NewReader(beginBody))
		begin.Header.Set("Idempotency-Key", "test")
		admitted := httptest.NewRecorder()
		s.ServeHTTP(admitted, begin)
		require.Equal(t, 202, admitted.Code, admitted.Body.String())
		var fenceRecord persistence.HostFenceRecord
		require.NoError(t, json.Unmarshal(admitted.Body.Bytes(), &fenceRecord))
		request = fenceRecord.Request
		digest, err := request.Digest()
		require.NoError(t, err)
		receipt, err := controlplane.SignHostFenceReceipt(controller, request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: request.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
		require.NoError(t, err)
		require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
		authorize := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest("POST", "/v1/agents/host-a/gc-fences/"+request.ID+"/authorize", bytes.NewBufferString(`{}`))
			require.NoError(t, agent.SignRequest(req, "host-a", secret, time.Now()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			return w
		}
		response := authorize()
		require.Equal(t, 200, response.Code, response.Body.String())
		proof, err := controlplane.SignGCDisposition(key, controlplane.GCDispositionProof{Request: request, Fence: receipt, Candidate: c, HostBootID: receipt.NewBootID, Presence: "absent", ObservedAt: time.Now().UTC()})
		require.NoError(t, err)
		if stage != "none" {
			input := gcDispositionCrashInput{Stage: stage, Proof: proof, HostKey: host}
			switch v := store.(type) {
			case *controlstore.Local:
				input.Kind = "file"
				input.Path = v.RunsDir
			case *controlstore.SQLite:
				input.Kind = "sqlite"
				input.Path = v.DbPath
			case *controlstore.Postgres:
				input.Kind = "postgres"
				input.Path = v.DSN
			}
			path := filepath.Join(t.TempDir(), "crash.json")
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			child := exec.Command(os.Args[0], "-test.run=^TestGCDispositionCrashChild$")
			child.Env = append(os.Environ(), "SYSBOX_GC_DISPOSITION_CHILD="+path)
			output, err := child.CombinedOutput()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, string(output))
			require.Equal(t, 73, exited.ExitCode(), string(output))
			held, err := authority.InspectGrant(ctx, "host-a", grant.Scope)
			require.NoError(t, err)
			require.Equal(t, controlplane.GrantRevoked, held.State)
		}
		bad := proof
		bad.Candidate.ExternalID = "other"
		require.Error(t, s.reconcileGCFence(ctx, bad))
		require.NoError(t, s.reconcileGCFence(ctx, proof))
		require.NoError(t, s.reconcileGCFence(ctx, proof))
		stored, err := repo.LoadGCReservation(ctx, "lab", reservation.ID)
		require.NoError(t, err)
		require.False(t, stored.Pending())
		require.Empty(t, stored.ReceiptHash)
		require.NotEmpty(t, stored.DispositionHash)
		record, err := readFailureCommand(ctx, store, cmd.ID)
		require.NoError(t, err)
		require.True(t, record.Retired)
		require.Equal(t, controlplane.AgentCommandStatusFailed, record.Command.Status)
		require.Error(t, authority.Authorize(ctx, *grant))
		require.Equal(t, 409, authorize().Code, "released fence cannot authorize journal rebuilding")
		raw, err := json.Marshal(proof)
		require.NoError(t, err)
		require.NotContains(t, string(raw), `"operation_result":"succeeded"`)
	})
}

type gcDispositionCrashInput struct {
	Stage, Kind, Path string
	Proof             controlplane.GCDispositionProof
	HostKey           ed25519.PublicKey
}

func TestGCDispositionCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_GC_DISPOSITION_CHILD")
	if path == "" {
		t.Skip("child process only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input gcDispositionCrashInput
	require.NoError(t, json.Unmarshal(raw, &input))
	var store controlstore.Store
	switch input.Kind {
	case "file":
		store = &controlstore.Local{RunsDir: input.Path}
	case "sqlite":
		store = &controlstore.SQLite{DbPath: input.Path, RunsDir: t.TempDir()}
	case "postgres":
		store = &controlstore.Postgres{DSN: input.Path}
	default:
		t.Fatal("invalid child backend")
	}
	s := stateDeleteTestServer(t, store)
	ctx := context.Background()
	p := input.Proof
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
	require.NoError(t, authority.PrepareGCDisposition(ctx, p, input.HostKey))
	if input.Stage == "prepared" {
		os.Exit(73)
	}
	commands, err := s.fenceCommandStorage()
	require.NoError(t, err)
	_, err = commands.UpdateCommand(ctx, p.Request.Grant.CommandID, func(c *persistence.CommandRecord) (*persistence.CommandRecord, error) {
		if c == nil {
			return nil, persistence.ErrNotFound
		}
		c.Retired = true
		c.Command.Status = controlplane.AgentCommandStatusFailed
		c.Command.Err = "host fenced; original GC outcome unknown"
		c.Command.EndedAt = p.ObservedAt
		return c, nil
	})
	require.NoError(t, err)
	if input.Stage == "command-retired" {
		os.Exit(73)
	}
	repo, err := s.actionRepository(p.Request.Topology)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteGCDisposition(ctx, p.Request.Topology, p, input.HostKey))
	os.Exit(73)
}

func TestCanonicalGCClaimSurvivesLostHostJournal(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.agentSvc = newAgentService(s)
		secret := "claim-secret"
		require.NoError(t, store.SaveAgent(ctx, controlplane.Agent{ID: "host", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
		_, issuer, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		repo, err := s.actionRepository("lab")
		require.NoError(t, err)
		refs, err := repo.LoadGCReferences(ctx, "lab")
		require.NoError(t, err)
		c := controlplane.GCCandidate{Kind: "firecracker", ExternalID: "external", OwnerUID: refs.TopologyUID, TopologyUID: refs.TopologyUID, ResourceUID: "resource", Generation: 1, InventoryDigest: refs.Digest}
		rsv := persistence.GCReservation{ID: "gc-once", AgentID: "host", Candidate: c}
		require.NoError(t, s.reserveGCWithAuthority(ctx, "lab", rsv))
		require.NoError(t, s.publishGCReservation(ctx, "lab", rsv))
		authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: store})
		session, err := authority.OpenSession(ctx, "host", "worker", "")
		require.NoError(t, err)
		cmd, ok, err := store.AcquireAgentCommandLease(ctx, "host", rsv.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		grant, err := s.issueCommandGrant(ctx, "host", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"})
		require.NoError(t, err)
		signed, err := controlplane.SignExecutionGrant(issuer, *grant, "boot", time.Now())
		require.NoError(t, err)
		claim := func() *httptest.ResponseRecorder {
			raw, _ := json.Marshal(map[string]any{"candidate": c, "grant": signed})
			request := httptest.NewRequest("POST", "/v1/agents/host/gc-claim", bytes.NewReader(raw))
			require.NoError(t, agent.SignRequest(request, "host", secret, time.Now()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request)
			return w
		}
		first := claim()
		require.Equal(t, 200, first.Code, first.Body.String())
		require.Equal(t, 409, claim().Code)
		cold := stateDeleteTestServer(t, store)
		cold.agentSvc = newAgentService(cold)
		require.NoError(t, cold.ConfigureExecutionGrantSigner(issuer))
		s = cold
		require.Equal(t, 409, claim().Code)
		retained, err := repo.LoadGCReservation(ctx, "lab", rsv.ID)
		require.NoError(t, err)
		require.NotEmpty(t, retained.AdmissionDigest)
		require.Empty(t, retained.ReceiptHash)
		require.True(t, retained.Pending())
		controllerPub, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.fenceTargets = map[string]hostFenceTarget{"host": {LabID: "isolated", PublicKey: controllerPub}}
		// A revoked command cannot acquire the one-shot execution after freeze.
		require.NoError(t, authority.Revoke(ctx, *grant))
		require.Error(t, s.claimGCExecution(ctx, "lab", c, *grant))

	})
}
