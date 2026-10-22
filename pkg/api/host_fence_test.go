package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestHostFenceAPIRecordsEvidenceWithoutCompletingRun(t *testing.T) {
	runHostFenceAPIContract(t, "", false)
}

func TestHostFenceDisposableVMIntake(t *testing.T) {
	root := os.Getenv("SYSBOX_HOST_FENCE_ACCEPTANCE_ROOT")
	if root == "" {
		t.Skip("requires explicitly scoped disposable acceptance lab")
	}
	runHostFenceAPIContract(t, root, false)
}

func runHostFenceAPIContract(t *testing.T, acceptanceRoot string, releaseCase bool, reconcileCase ...bool) {
	reconcile := len(reconcileCase) > 0 && reconcileCase[0]
	intentCase := len(reconcileCase) > 1 && reconcileCase[1]
	resetIntent := len(reconcileCase) > 3 && reconcileCase[3]
	incompleteCreate := len(reconcileCase) > 4 && reconcileCase[4]
	incompleteReplacement := len(reconcileCase) > 5 && reconcileCase[5]
	withChild := len(reconcileCase) > 6 && reconcileCase[6]
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint("changed-incarnation-", mismatch), func(t *testing.T) {
			eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
				if acceptanceRoot != "" {
					if _, ok := store.(*localAPIStore); !ok || mismatch {
						t.Skip("real VM round uses one local canonical fixture")
					}
				}
				ctx := context.Background()
				s := stateDeleteTestServer(t, store)
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				labID, oldBoot := "isolated-test", "11111111-1111-4111-8111-111111111111"
				if acceptanceRoot != "" {
					manifestRaw, err := os.ReadFile(filepath.Join(acceptanceRoot, "lab.json"))
					require.NoError(t, err)
					var manifest struct {
						ID string `json:"id"`
					}
					require.NoError(t, json.Unmarshal(manifestRaw, &manifest))
					labID = manifest.ID
					boot, err := exec.Command("/usr/bin/python3", "-c", "import sys;sys.path.insert(0,'../../scripts/acceptance');import host_fencing as h;r,m=h.two_vms.load(sys.argv[1]);h.check_scope(r,m);print(h.wait_boot(r,m,'execution'))", acceptanceRoot).CombinedOutput()
					require.NoError(t, err, string(boot))
					oldBoot = strings.TrimSpace(string(boot))
				}
				s.fenceTargets = map[string]hostFenceTarget{"agent": {LabID: labID, PublicKey: pub}}
				repo, err := s.actionRepository("lab")
				require.NoError(t, err)
				st, err := repo.LoadState(ctx, "lab")
				require.NoError(t, err)
				run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Op: "destroy", Operation: "destroy", Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Revision: "revision", Attempt: 1, LeaseOwner: "owner"}
				if incompleteCreate {
					run.Op = "apply"
					run.Operation = "apply"
				}
				if resetIntent {
					run.Op = "reset"
					run.Operation = "reset"
				}
				if releaseCase && !reconcile {
					run.Status = controlplane.RunFailed
					run.EndedAt = time.Now().UTC()
				}
				require.NoError(t, store.SaveRun(ctx, run))
				grantTestTopologyUID(t, s, "another")
				pending := controlplane.Run{ID: "pending-other", Topology: "another", AgentID: "agent", Op: "apply", Status: controlplane.RunAssigned, QueuedAt: time.Now().UTC()}
				require.NoError(t, store.SaveRun(ctx, pending))
				action := runtime.ActionRecord{ID: "action", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, OwnerUID: st.TopologyUID, ResourceUID: "resource", Generation: 1, Resource: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted}
				if releaseCase {
					action.Action = controlplane.PlanActionDelete
					action.ProviderOperationID = action.ID
					action.ProviderOperationAnchor = "action:" + action.ID
					action.Quiescence = "unknown"
					action.Candidate = &state.Resource{Driver: "docker", Address: action.Resource, OwnerUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: action.Generation, ExternalID: "external"}
					if intentCase && acceptanceRoot == "" {
						require.NoError(t, action.Candidate.SetProviderState(json.RawMessage(`{"identity":"fixture"}`)))
					}
					if reconcile && acceptanceRoot != "" {
						provider := os.Getenv("SYSBOX_DELETE_CLOSURE_PROVIDER")
						if provider == "" {
							provider = "docker"
						}
						output, err := exec.Command("/usr/bin/python3", "../../scripts/acceptance/delete_closure.py", "fixture", "--root", acceptanceRoot, "--provider", provider).CombinedOutput()
						require.NoError(t, err, string(output))
						var fixture struct {
							Driver        string          `json:"driver"`
							ExternalID    string          `json:"external_id"`
							ProviderState json.RawMessage `json:"provider_state"`
						}
						require.NoError(t, json.Unmarshal(output, &fixture))
						action.Candidate.Driver = fixture.Driver
						action.Candidate.ExternalID = fixture.ExternalID
						if len(fixture.ProviderState) > 0 {
							var providerState map[string]any
							require.NoError(t, json.Unmarshal(fixture.ProviderState, &providerState))
							providerState["identity"] = map[string]any{"action_id": action.ID, "owner_uid": action.OwnerUID, "topology_uid": action.OwnerUID, "resource_uid": action.ResourceUID, "generation": action.Generation}
							encoded, err := json.Marshal(providerState)
							require.NoError(t, err)
							require.NoError(t, action.Candidate.SetProviderState(encoded))
						}
					}

					if !incompleteCreate {
						seed := action
						seed.ID = "seed"
						seed.RunID = "older-run"
						seed.Action = controlplane.PlanActionCreate
						require.NoError(t, repo.CommitActionIntent(ctx, seed))
						records, err := repo.LoadActions(ctx, "lab")
						require.NoError(t, err)
						seed = records[0]
						seed.Status = runtime.ActionSucceeded
						require.NoError(t, repo.CommitActionOutcome(ctx, seed, &runtime.StatePatch{Resource: seed.Resource.String(), Action: seed.Action, Op: runtime.StatePatchUpsert}))
						current, err := repo.LoadState(ctx, "lab")
						require.NoError(t, err)
						action.StateSerial = current.Meta.Serial
						action.ExpectedSerial = current.Meta.Serial
					}

				}
				if incompleteCreate {
					action.Action = controlplane.PlanActionCreate
					action.ReplayForbidden = true
				}
				if resetIntent {
					action.Action = controlplane.PlanActionReset
					action.Generation = 2
					action.Phase = "accepted"
					action.ReplayForbidden = true
				}
				require.NoError(t, repo.CommitActionIntent(ctx, action))
				if resetIntent {
					phases := []string{"preparing", "prepared", "destroying"}
					if incompleteReplacement {
						phases = append(phases, "destroy_returned", "destroyed", "applying")
					}
					for _, phase := range phases {
						records, err := repo.LoadActions(ctx, "lab")
						require.NoError(t, err)
						for _, current := range records {
							if current.ID == action.ID {
								current.Phase = phase
								current.Status = runtime.ActionUncertain
								if incompleteReplacement && phase == "applying" {
									replacement := *current.Candidate
									replacement.Generation = current.Generation
									replacement.ExternalID = "replacement"
									if acceptanceRoot != "" {
										replacement = createIntentReplacementFixture(t, acceptanceRoot, current)
									}
									current.Replacement = &replacement
								}
								require.NoError(t, repo.CommitActionOutcome(ctx, current, nil))
							}
						}
					}
				}

				if releaseCase && !reconcile {
					records, err := repo.LoadActions(ctx, "lab")
					require.NoError(t, err)
					completed := records[0]
					for _, record := range records {
						if record.ID == action.ID {
							completed = record
						}
					}
					completed.Status = runtime.ActionSucceeded
					require.NoError(t, repo.CommitActionOutcome(ctx, completed, &runtime.StatePatch{Resource: completed.Resource.String(), Action: completed.Action, Op: runtime.StatePatchDelete}))
				}
				authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store})
				session, err := authority.OpenSession(ctx, "agent", "process", "")
				require.NoError(t, err)
				original := run
				if mismatch {
					original.QueuedAt = original.QueuedAt.Add(-time.Hour)
				}
				cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "run_assigned", Run: &original, ActionID: run.ID, TopologyUID: st.TopologyUID, ExpectedSerial: 1, Attempt: 1}
				require.NoError(t, store.SaveAgentCommand(ctx, cmd))
				cmd.LeaseOwner, cmd.LeaseUntil = "owner", time.Now().UTC().Add(time.Minute)
				source, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{Attempt: 1, LeaseOwner: "owner", SessionID: session.ID, BootID: oldBoot}, &persistence.CommandRecord{Command: cmd})
				require.NoError(t, err)
				grant, err := authority.Issue(ctx, source, time.Minute)
				require.NoError(t, err)
				var children []controlplane.ExecutionGrant
				if withChild {
					child, err := authority.DeriveGuestTask(ctx, *grant, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: grant.Scope.TopologyUID, ResourceUID: "resource", Generation: 1}, AgentID: grant.AgentID, SessionID: grant.SessionID, CommandID: grant.CommandID, ActionID: "setup-task", Attempt: grant.Attempt, Operation: "guest_task", Digest: "task-digest", TargetBootID: "old-guest-boot"}, time.Minute)
					require.NoError(t, err)
					children = append(children, *child)
				}
				raw, err := json.Marshal(map[string]any{"run_id": "run", "grant": grant})
				require.NoError(t, err)
				request := httptest.NewRequest("POST", "/v1/topologies/lab/host-fences", bytes.NewReader(raw))
				request.Header.Set("Idempotency-Key", "fence-one")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, request)
				if mismatch {
					require.Equal(t, 409, w.Code, w.Body.String())
					retained, err := authority.InspectGrant(ctx, "agent", grant.Scope)
					require.NoError(t, err)
					require.Equal(t, controlplane.GrantActive, retained.State)
					return
				}
				require.Equal(t, 202, w.Code, w.Body.String())
				_, claimed, err := store.ClaimRun(ctx, pending.ID, "agent", "worker", time.Minute)
				require.ErrorIs(t, err, persistence.ErrConflict)
				require.False(t, claimed)
				unrelated := controlplane.AgentCommand{ID: "unrelated", AgentID: "agent", Type: "run_assigned", TopologyUID: "another-owner"}
				require.ErrorIs(t, store.SaveAgentCommand(ctx, unrelated), persistence.ErrConflict)
				otherRun := run
				otherRun.ID, otherRun.Topology, otherRun.Status = "other-run", "other", controlplane.RunAssigned
				dispatch := incarnationDispatch(otherRun)
				dispatch.Command.TopologyUID = "other-owner"
				_, _, err = store.CreateRunDispatch(ctx, dispatch)
				require.ErrorIs(t, err, persistence.ErrConflict)
				var accepted persistence.HostFenceRecord
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &accepted))
				require.Equal(t, labID, accepted.Request.LabID)
				require.Len(t, accepted.Request.JournalSHA256, 64)
				checkFenceBeforeReceipt(t, s, accepted)
				digest, err := accepted.Request.Digest()
				require.NoError(t, err)
				receipt, err := controlplane.SignHostFenceReceipt(key, accepted.Request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: digest, LabID: accepted.Request.LabID, OldBootID: grant.TargetBootID, NewBootID: "22222222-2222-4222-8222-222222222222", ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
				require.NoError(t, err)
				if acceptanceRoot != "" {
					admissionPath := filepath.Join(acceptanceRoot, "api-fence-admission.json")
					keyPath := filepath.Join(acceptanceRoot, "api-fence-controller.key")
					require.NoError(t, os.WriteFile(admissionPath, w.Body.Bytes(), 0600))
					require.NoError(t, os.WriteFile(keyPath, key, 0600))
					ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
					defer cancel()
					output, err := exec.CommandContext(ctx, "/usr/bin/python3", "../../scripts/acceptance/host_fencing.py", "fence", "--root", acceptanceRoot, "--admission", admissionPath, "--attestor", "/tmp/sysbox-fence-attestor", "--controller-key", keyPath).CombinedOutput()
					require.NoError(t, err, string(output))
					t.Log(string(output))
					lines := strings.Split(strings.TrimSpace(string(output)), "\n")
					var result struct {
						Evidence string `json:"evidence"`
					}
					require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &result))
					raw, err := os.ReadFile(strings.TrimSuffix(result.Evidence, ".json") + ".receipt.json")
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(raw, &receipt))
				}
				post := func(v controlplane.HostFenceReceipt) *httptest.ResponseRecorder {
					raw, err := json.Marshal(v)
					require.NoError(t, err)
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/topologies/lab/host-fences/"+accepted.Request.ID+"/receipt", bytes.NewReader(raw)))
					return w
				}
				bad := receipt
				bad.NewBootID = receipt.OldBootID
				require.Equal(t, 409, post(bad).Code)
				got := post(receipt)
				require.Equal(t, 200, got.Code, got.Body.String())
				if acceptanceRoot != "" {
					require.NoError(t, os.WriteFile(filepath.Join(acceptanceRoot, "api-fence-accepted.json"), got.Body.Bytes(), 0600))
				}
				require.Equal(t, 200, post(receipt).Code)
				if releaseCase {
					if intentCase {
						checkFenceIntentClosure(t, s, store, accepted, len(reconcileCase) > 2 && reconcileCase[2], acceptanceRoot, children...)
						return
					}
					if reconcile {
						checkFenceDeleteClosure(t, s, store, accepted, acceptanceRoot)
					} else {
						checkCompletedFenceRelease(t, s, store, accepted)
					}
					return
				}
				current, err := store.GetRun(ctx, run.ID)
				require.NoError(t, err)
				require.Equal(t, controlplane.RunRunning, current.Status)
				actions, err := repo.LoadActions(ctx, "lab")
				require.NoError(t, err)
				require.Equal(t, runtime.ActionIntentCommitted, actions[0].Status)
				retained, err := authority.InspectGrant(ctx, "agent", grant.Scope)
				require.NoError(t, err)
				require.Equal(t, controlplane.GrantRevoked, retained.State)
				require.Empty(t, retained.StopReceipt)
				barrier, err := authority.InspectMaintenance(ctx, accepted.BarrierID)
				require.NoError(t, err)
				require.ErrorIs(t, authority.EndMaintenance(ctx, *barrier), persistence.ErrConflict)
				cold := stateDeleteTestServer(t, store)
				read := httptest.NewRecorder()
				cold.ServeHTTP(read, httptest.NewRequest("GET", "/v1/topologies/lab/host-fences/"+accepted.Request.ID, nil))
				require.Equal(t, 200, read.Code)
				require.Contains(t, read.Body.String(), "receipt")
				checkFenceObservationContract(t, s, store, accepted)
			})
		})
	}
}

func TestHostFenceControllerCannotReuseAnyHostKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "controller.pub")
	require.NoError(t, os.WriteFile(path, pub, 0600))
	s := &Server{cfg: config.ServiceConfig{}, grantHostKeys: map[string]ed25519.PublicKey{"other-agent": pub}}
	s.cfg.API.ExecutionGrant.DisposableFenceTargets = map[string]config.DisposableFenceTarget{"agent": {LabID: "isolated", ControllerPublicKeyFile: path}}
	require.Error(t, s.loadHostFenceTargets())
}

func TestHostFenceReleaseAPI(t *testing.T) { runHostFenceAPIContract(t, "", true) }

func TestHostFenceDeleteClosureAPI(t *testing.T) { runHostFenceAPIContract(t, "", true, true) }

func TestHostFenceDeleteClosureDisposableVM(t *testing.T) {
	root := os.Getenv("SYSBOX_HOST_FENCE_ACCEPTANCE_ROOT")
	if root == "" {
		t.Skip("explicit disposable lab required")
	}
	runHostFenceAPIContract(t, root, true, true)
}
