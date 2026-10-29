package hostexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type recoveryTestBridge struct {
	Bridge
	repo runtime.ActionRepository
}

func (b recoveryTestBridge) ActionRepository(string) (runtime.ActionRepository, error) {
	return b.repo, nil
}

func (b recoveryTestBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected revision materialization during observation")
}

type recoveryTestRepo struct {
	st      *state.State
	actions []runtime.ActionRecord
}

func (r *recoveryTestRepo) LoadState(context.Context, string) (*state.State, error) { return r.st, nil }
func (r *recoveryTestRepo) LoadActions(context.Context, string) ([]runtime.ActionRecord, error) {
	return r.actions, nil
}
func (r *recoveryTestRepo) CommitActionIntent(context.Context, runtime.ActionRecord) error {
	return errors.New("unexpected write")
}
func (r *recoveryTestRepo) CommitActionOutcome(context.Context, runtime.ActionRecord, *runtime.StatePatch) error {
	return errors.New("unexpected write")
}

func TestHostRecoveryRejectsRevocationAfterObservation(t *testing.T) {
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Revision: "revision"}
	repo := &recoveryTestRepo{st: &state.State{TopologyUID: "owner"}, actions: []runtime.ActionRecord{{ID: "action", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Status: runtime.ActionUncertain}}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			http.Error(w, "lease revoked", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(controlplane.RunRecoveryAuthorization{Run: run, Mode: "observe", LeaseUntil: time.Now().Add(time.Minute)})
	}))
	defer server.Close()
	host := &RunExecutor{Bridge: recoveryTestBridge{repo: repo}, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent"}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: server.URL}}}
	report, err := host.Recover(context.Background(), hostsupervisor.RunRecoveryRequest{CommandID: "observe", LeaseOwner: "delivery", Attempt: 1})
	require.ErrorContains(t, err, "HTTP 409")
	require.Nil(t, report)
}

func TestHostFenceObservationRejectsChangedBindingAfterRead(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint("drop-", drop), func(t *testing.T) {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			binding := controlplane.FenceObservationBinding{RequestSHA256: strings.Repeat("a", 64), HostBootID: "11111111-1111-4111-8111-111111111111"}
			run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", Status: controlplane.RunRunning, Attempt: 1, QueuedAt: time.Now().UTC(), Revision: "revision"}
			repo := &recoveryTestRepo{st: &state.State{TopologyUID: "owner"}, actions: []runtime.ActionRecord{{ID: "action", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "web"), OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Status: runtime.ActionUncertain}}}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				current := binding
				auth := controlplane.RunRecoveryAuthorization{Run: run, Mode: controlplane.RunRecoveryObserve, LeaseUntil: time.Now().Add(time.Minute), FenceObservation: &current}
				if calls > 1 {
					if drop {
						auth.FenceObservation = nil
					} else {
						current.RequestSHA256 = strings.Repeat("b", 64)
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(auth))
			}))
			defer server.Close()
			host := RunExecutor{Bridge: recoveryTestBridge{repo: repo}, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent", HostBootID: binding.HostBootID, HostKey: key}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: server.URL}}}
			report, err := host.Recover(context.Background(), hostsupervisor.RunRecoveryRequest{CommandID: "observe", LeaseOwner: "worker", Attempt: 1})
			require.ErrorContains(t, err, "authorization changed")
			require.Nil(t, report)
			require.Equal(t, 2, calls)
		})
	}
}

func TestMissingIdentityObservationLoadsOnlyPinnedWorkspace(t *testing.T) {
	prior := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = prior })
	d, err := docker.New()
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Node: d, NIC: d, NodeState: d}))
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	hcl := []byte("resource \"sysbox_node\" \"node\" {\n image=\"\"\n substrate=\"docker\"\n}\n")
	require.NoError(t, os.WriteFile(path, hcl, 0600))
	files := map[string][]byte{"field.sysbox.hcl": hcl}
	revision := controlplane.GlobalRevision{Files: files, Revision: controlplane.ComputeRevisionDigest(files)}
	local := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir})
	repo, err := local.ActionRepository("lab")
	require.NoError(t, err)
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", QueuedAt: time.Now().UTC(), Revision: revision.Revision, Attempt: 1}
	action := runtime.ActionRecord{ID: "missing", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "node"), Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, DesiredDigest: "pinned", StateSerial: st.Meta.Serial, ExpectedSerial: st.Meta.Serial}
	require.NoError(t, repo.CommitActionIntent(context.Background(), action))
	bridge := &pinnedHostRecoveryBridge{observedAgentBridge: &observedAgentBridge{LocalBridge: local, repository: repo}, path: path, revision: revision.Revision}
	cmd := controlplane.AgentCommand{ID: "observe", AgentID: "agent", Run: &run, RecoveryMode: controlplane.RunRecoveryObserve}
	report, err := NewExecutorWithBridge(bridge).ObserveRecoverRun(context.Background(), "agent", cmd, controlplane.RunRecoveryAuthorization{Run: run, Mode: controlplane.RunRecoveryObserve, Revision: &revision})
	require.NoError(t, err)
	require.True(t, bridge.materialized)
	require.Equal(t, "unknown", report.Resources[0].Status, "missing driver/digest never proves absence")
	records, err := repo.LoadActions(context.Background(), "lab")
	require.NoError(t, err)
	require.Nil(t, records[0].Candidate)
	bridge.materialized = false
	_, err = NewExecutorWithBridge(bridge).ObserveRecoverRun(context.Background(), "agent", cmd, controlplane.RunRecoveryAuthorization{Run: run, Mode: controlplane.RunRecoveryObserve})
	require.NoError(t, err)
	require.False(t, bridge.materialized, "legacy observation does not invent pinned inputs")
}
