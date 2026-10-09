package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func guestHostReceiptForTest(t *testing.T, cmd controlplane.AgentCommand, result controlplane.GuestExecutionResult) controlplane.SignedExecutionStopReceipt {
	t.Helper()
	task, err := guesttask.FromExecutionCommand(cmd)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	result.Encoding = "base64"
	return controlplane.SignedExecutionStopReceipt{Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}, CommandID: cmd.ID, ActionID: cmd.ActionID, AgentID: cmd.AgentID, Attempt: 1, Epoch: 1, TargetBootID: "guest", Operation: "guest_task", Digest: digest}, TaskID: cmd.ActionID, TaskDigest: digest, GuestBootID: "guest", Quiescence: "confirmed", StoppedAt: time.Now().UTC(), GuestResult: &controlplane.GuestTaskResult{Result: result, ExitCodeKnown: true, ResultClass: "exit"}}
}

func TestHostGuestHTTPCompletionAfterCancellationRetirementAndRestart(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC(), Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
		cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ExecutionRequest: x.Request, TopologyUID: grantTestTopologyUID(t, s, "lab"), ResourceUID: "resource", Generation: 1, ActionID: x.ID}
		require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
		leased, ok, err := store.AcquireAgentCommandLease(ctx, "agent", cmd.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		receipt := guestHostReceiptForTest(t, cmd, controlplane.GuestExecutionResult{ExitCode: 7, Stdout: "b3V0"})
		request := receipt.Grant
		request.SessionID = session.ID
		request.Attempt = uint64(leased.Attempt)
		grant, err := authority.Issue(ctx, request, time.Minute)
		require.NoError(t, err)
		_, err = store.CancelGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.NoError(t, store.DeleteAgentCommand(ctx, "agent", cmd.ID))
		receipt, err = controlplane.SignExecutionTaskStopReceipt(key, *grant, "host", "guest", x.ID, grant.Digest, *receipt.GuestResult, "receipt", time.Now())
		require.NoError(t, err)
		post := func(server *Server, r controlplane.SignedExecutionStopReceipt) *httptest.ResponseRecorder {
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
			return w
		}
		tampered := receipt
		out := *receipt.GuestResult
		out.Result.ExitCode++
		tampered.GuestResult = &out
		require.Equal(t, 401, post(s, tampered).Code)
		untouched, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "queued", untouched.Status)
		response := post(s, receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		completed, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, "completed", completed.Status)
		require.Equal(t, 7, completed.Result.ExitCode)
		require.True(t, completed.CancelRequested)
		completed.Request = controlplane.GuestExecutionRequest{}
		completed.Result.Stdout = ""
		ok, err = store.CompareAndSwapGuestExecution(ctx, *completed, completed.Version)
		require.NoError(t, err)
		require.True(t, ok)
		successorRequest := *grant
		successorRequest.ActionID = "successor"
		successorRequest.ExpiresAt = time.Time{}
		successor, err := authority.Issue(ctx, successorRequest, time.Minute)
		require.NoError(t, err)
		cold := stateDeleteTestServer(t, store)
		cold.grantHostKeys = s.grantHostKeys
		response = post(cold, receipt)
		require.Equal(t, 200, response.Code, response.Body.String())
		retained, err := store.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Empty(t, retained.Result.Stdout, "late replay must not restore expired output")
		require.NoError(t, authority.Authorize(ctx, *successor), "old replay cannot revoke successor")
	})
}

func TestHostGuestCompletionRetriesBothCommitBoundaries(t *testing.T) {
	for _, stage := range []string{"completion", "release"} {
		t.Run(stage, func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				if _, local := store.(*localAPIStore); local {
					t.Skip("SQL write fault injection")
				}
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
				x := controlplane.GuestExecution{ID: "execution", CommandID: "command", AgentID: "agent", Topology: "lab", Node: "node", Version: 1, Status: "queued", CreatedAt: time.Now().UTC(), Request: controlplane.GuestExecutionRequest{Argv: []string{"true"}}}
				cmd := controlplane.AgentCommand{ID: x.CommandID, AgentID: x.AgentID, Type: "guest_execution", Execution: &x, ExecutionRequest: x.Request, TopologyUID: grantTestTopologyUID(t, s, "lab"), ResourceUID: "resource", Generation: 1, ActionID: x.ID}
				require.NoError(t, store.CreateGuestExecutionDispatch(ctx, x, cmd))
				authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
				session, err := authority.OpenSession(ctx, "agent", "host", "")
				require.NoError(t, err)
				receipt := guestHostReceiptForTest(t, cmd, controlplane.GuestExecutionResult{ExitCode: 7})
				request := receipt.Grant
				request.SessionID = session.ID
				grant, err := authority.Issue(ctx, request, time.Minute)
				require.NoError(t, err)
				receipt, err = controlplane.SignExecutionTaskStopReceipt(key, *grant, "host", "guest", x.ID, grant.Digest, *receipt.GuestResult, "receipt", time.Now())
				require.NoError(t, err)
				var remove func()
				switch dbstore := store.(type) {
				case *sqliteAPIStore:
					db, err := dbstore.open()
					require.NoError(t, err)
					table, when := "sysbox_guest_executions", ""
					if stage == "release" {
						table = "sysbox_execution_authority"
						when = ` WHEN CAST(NEW.data AS TEXT) LIKE '%"stopped"%'`
					}
					_, err = db.ExecContext(ctx, "CREATE TRIGGER reject_host_commit BEFORE UPDATE ON "+table+when+" BEGIN SELECT RAISE(ABORT,'injected host commit failure'); END")
					require.NoError(t, err)
					remove = func() { _, err := db.ExecContext(ctx, "DROP TRIGGER reject_host_commit"); require.NoError(t, err) }
				case *postgresAPIStore:
					pool, err := dbstore.poolFor(ctx)
					require.NoError(t, err)
					table, check := "sysbox_guest_executions", "TRUE"
					if stage == "release" {
						table = "sysbox_execution_authority"
						check = `NEW.data::text LIKE '%"stopped"%'`
					}
					_, err = pool.Exec(ctx, "CREATE FUNCTION reject_host_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF "+check+" THEN RAISE EXCEPTION 'injected host commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_host_commit BEFORE UPDATE ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_host_commit()")
					require.NoError(t, err)
					remove = func() { _, err := pool.Exec(ctx, "DROP TRIGGER reject_host_commit ON "+table); require.NoError(t, err) }
				}
				post := func() *httptest.ResponseRecorder {
					raw, err := json.Marshal(receipt)
					require.NoError(t, err)
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
					return w
				}
				require.Equal(t, 500, post().Code)
				held, err := authority.InspectGrant(ctx, "agent", grant.Scope)
				require.NoError(t, err)
				require.Equal(t, controlplane.GrantRevoked, held.State, "failure must not release exclusion")
				current, err := store.GetGuestExecution(ctx, x.ID)
				require.NoError(t, err)
				if stage == "completion" {
					require.Equal(t, "queued", current.Status)
				} else {
					require.Equal(t, "completed", current.Status)
				}
				remove()
				response := post()
				require.Equal(t, 200, response.Code, response.Body.String())
				finished, err := authority.InspectGrant(ctx, "agent", grant.Scope)
				require.NoError(t, err)
				require.Equal(t, controlplane.GrantStopped, finished.State)
				response = post()
				require.Equal(t, 200, response.Code, response.Body.String())
			})
		})
	}
}

func TestHostRunChildStopDoesNotRequireStandaloneExecution(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"agent": pub}
		authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
		session, err := authority.OpenSession(ctx, "agent", "host", "")
		require.NoError(t, err)
		parent, err := authority.Issue(ctx, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology"}, AgentID: "agent", SessionID: session.ID, CommandID: "run-command", ActionID: "run", Attempt: 1, Operation: "run_apply", Digest: "run-digest"}, time.Minute)
		require.NoError(t, err)
		child, err := authority.DeriveGuestTask(ctx, *parent, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "node", Generation: 1}, AgentID: "agent", SessionID: session.ID, CommandID: parent.CommandID, ActionID: "run-task", Attempt: 1, ParentEpoch: parent.Epoch, Operation: "guest_task", Digest: "task-digest", TargetBootID: "guest"}, 30*time.Second)
		require.NoError(t, err)
		receipt, err := controlplane.SignExecutionTaskStopReceipt(key, *child, "host", "guest", child.ActionID, child.Digest, controlplane.GuestTaskResult{Result: controlplane.GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}, "child-stop", time.Now())
		require.NoError(t, err)
		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/hosts/agent/execution-grants/stopped", bytes.NewReader(raw)))
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NoError(t, authority.Authorize(ctx, *parent))
	})
}
