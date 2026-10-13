package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestNeverEnteredRunRecoveryCommitsFailureAndRetiresAuthority(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := NewServer(t.TempDir(), t.TempDir())
		s.apiStore = store
		owner := grantTestTopologyUID(t, s, "lab")
		issuerPublic, issuer, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		hostPublic, host, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": hostPublic}
		now := time.Now().UTC()
		run := controlplane.Run{ID: "unentered", Topology: "lab", Op: "destroy", Operation: "destroy", Revision: "revision", AgentID: "agent", Status: controlplane.RunRunning, Attempt: 1, LeaseOwner: "worker", QueuedAt: now, LeaseUntil: now.Add(time.Minute)}
		require.NoError(t, store.SaveRun(ctx, run))
		cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusRunning, Run: &run, TopologyUID: owner, ActionID: run.ID, Attempt: 1, LeaseOwner: "worker", LeaseUntil: run.LeaseUntil}
		require.NoError(t, store.SaveAgentCommand(ctx, cmd))
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "original", "")
		require.NoError(t, err)
		source, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: session.ID, Attempt: 1, LeaseOwner: "worker", BootID: "original-boot"}, &persistence.CommandRecord{Command: cmd})
		require.NoError(t, err)
		grant, err := authority.Issue(ctx, source, time.Minute)
		require.NoError(t, err)
		signed, err := controlplane.SignExecutionGrant(issuer, *grant, "original-boot", now)
		require.NoError(t, err)
		root := t.TempDir()
		require.NoError(t, os.Chmod(root, 0700))
		gate := &effectgate.Gate{Root: root, PublicKey: issuerPublic, BootID: "original-boot", Authorize: func(context.Context, controlplane.ExecutionGrant) error { return nil }}
		req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: owner, Revision: run.Revision, ActionID: cmd.ActionID, Action: run.Op}
		seed, err := json.Marshal(map[string]any{"version": 1, "request": req, "run": run})
		require.NoError(t, err)
		_, err = gate.ReserveRunWithAdmission(ctx, signed, cmd.ActionID, seed)
		require.NoError(t, err)
		cold := &hostsupervisor.RunCoordinator{Root: root, IssuerKey: issuerPublic, HostKey: host, AgentID: "agent", HostBootID: "new-boot"}
		cold.ReportRunID = func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
			body, err := json.Marshal(hostRunCompletion{Receipt: receipt, Evidence: evidence})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/hosts/agent/runs/"+id+"/completed", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			return nil
		}
		require.NoError(t, cold.Recover(ctx))
		actual, err := store.GetRun(ctx, run.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.RunFailed, actual.Status)
		require.NotNil(t, actual.HostCompletion)
		require.Nil(t, actual.Cleanup, "failed Destroy is not evidence of empty resources")
		require.NoError(t, controlplane.VerifyRunStopReceipt(hostPublic, actual.HostCompletion.Receipt, actual.HostCompletion.Evidence))
		commands, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Empty(t, commands)
		require.Error(t, authority.Authorize(ctx, *grant))
		require.NoError(t, cold.Recover(ctx))
	})
}
