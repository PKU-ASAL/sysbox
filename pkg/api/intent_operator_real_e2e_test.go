//go:build e2e

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"

	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// Only initial interrupted records are fixture setup. Recovery and subsequent
// work use the public API/CLI and real independently running Agent/supervisor.
func TestStageOnePublicOperatorRecoveryE2E(t *testing.T) {
	labRoot, binary := os.Getenv("SYSBOX_GC_FENCE_LAB"), os.Getenv("SYSBOX_CLI_BINARY")
	require.NotEmpty(t, labRoot)
	require.NotEmpty(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	var lab struct {
		ID    string
		Ports map[string]int
	}
	raw, err := os.ReadFile(filepath.Join(labRoot, "lab.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &lab))
	var enrolled struct {
		LabID      string `json:"lab_id"`
		Disposable bool
	}
	raw, err = os.ReadFile(filepath.Join(labRoot, "fencing-scope.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &enrolled))
	require.True(t, enrolled.Disposable)
	require.Equal(t, lab.ID, enrolled.LabID)
	suffix := uuid.NewString()[:8]
	topology := "operator-" + suffix
	agentID := "operator-agent-" + suffix
	remote := "/tmp/sbo-" + suffix
	ssh := func(command string) string {
		t.Helper()
		callCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		out, err := exec.CommandContext(callCtx, "ssh", "-F", "/dev/null", "-i", filepath.Join(labRoot, "client"), "-p", fmt.Sprint(lab.Ports["execution"]), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(labRoot, "known_hosts"), "p9@127.0.0.1", command).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	upload := func(local, dest string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "scp", "-F", "/dev/null", "-q", "-i", filepath.Join(labRoot, "client"), "-P", fmt.Sprint(lab.Ports["execution"]), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(labRoot, "known_hosts"), local, "p9@127.0.0.1:"+dest).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	round := filepath.Join(labRoot, "operator-round-"+suffix)
	require.NoError(t, os.Mkdir(round, 0700))
	save := func(name string, v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		path := filepath.Join(round, name)
		require.NoError(t, os.WriteFile(path, raw, 0600))
		return path
	}
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	dsn := os.Getenv("SYSBOX_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn)
	pg, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	schema := "sysbox_operator_" + strings.ReplaceAll(suffix, "-", "")
	_, err = pg.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	require.NoError(t, pg.Close(ctx))
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	cfg.State.Backend = dsn + separator + "search_path=" + schema
	save("database-binding.json", map[string]string{"schema": schema, "purpose": "isolated operator acceptance"})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("failed operator schema retained for inspection:", schema)
			return
		}
		clean, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		conn, err := pgx.Connect(clean, dsn)
		if err != nil {
			t.Errorf("cleanup connection: %v", err)
			return
		}
		defer conn.Close(clean)
		_, err = conn.Exec(clean, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Errorf("cleanup isolated schema: %v", err)
		}
	})
	s := NewServerWithConfig(cfg)
	require.NoError(t, s.initErr)
	issuerPub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{agentID: hostPub}
	controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.fenceTargets = map[string]hostFenceTarget{agentID: {LabID: lab.ID, PublicKey: controllerPub}}
	token := uuid.NewString()
	serviceToken := uuid.NewString()
	secret := uuid.NewString()
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: agentID, AuthSecret: secret, SecretHash: agent.SecretHash(secret), Status: "online", Protocol: controlplane.AgentProtocolVersion, Capabilities: agent.DeriveCapabilities([]string{"docker"})}))
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Sysbox-Roles")
		r.Header.Del("X-Sysbox-User")
		credential := r.Header.Get("Authorization")
		if credential != "Bearer "+token && credential != "Bearer "+serviceToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if credential == "Bearer "+token {
			r.Header.Set("X-Sysbox-Roles", "admin")
		}
		if strings.HasSuffix(r.URL.Path, "/reconcile-intents") || strings.HasPrefix(r.URL.Path, "/v1/hosts/") || strings.HasSuffix(r.URL.Path, "/authorize") {
			recorder := httptest.NewRecorder()
			s.ServeHTTP(recorder, r)
			if recorder.Code >= 400 {
				t.Logf("request %s status=%d body=%s", r.URL.Path, recorder.Code, recorder.Body.String())
			}
			for k, values := range recorder.Header() {
				for _, value := range values {
					w.Header().Add(k, value)
				}
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		s.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint.Listener = listener
	endpoint.Start()
	t.Cleanup(endpoint.Close)
	port := listener.Addr().(*net.TCPAddr).Port
	localURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	guestURL := fmt.Sprintf("http://10.0.2.2:%d", port)
	request := func(method, path string, body any, key string) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		r, err := http.NewRequestWithContext(ctx, method, localURL+path, bytes.NewReader(raw))
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, data
	}
	hcl := `resource "sysbox_image" "base" {
 substrate="docker"
 source="alpine:3.22"
 kind="oci"
 architecture="amd64"
 guest_family="linux"
}
resource "sysbox_node" "node" {
 substrate="docker"
 image=sysbox_image.base.id
 provider "docker" {
  entrypoint=["sleep"]
  command=["infinity"]
 }
}`
	status, data := request("POST", "/v1/revisions", map[string]any{"files": map[string]string{"field.sysbox.hcl": hcl}}, "")
	require.Equal(t, 201, status, string(data))
	var revision struct{ Revision string }
	require.NoError(t, json.Unmarshal(data, &revision))
	repo, err := s.actionRepository(topology)
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, topology)
	require.NoError(t, err)
	oldBoot := ssh("cat /proc/sys/kernel/random/boot_id")
	original := controlplane.Run{ID: "original-" + suffix, Topology: topology, AgentID: agentID, Op: "apply", Operation: "apply", Status: controlplane.RunRunning, QueuedAt: time.Now().UTC(), Revision: revision.Revision, Attempt: 1, LeaseOwner: "interrupted", LeaseUntil: time.Now().Add(10 * time.Minute)}
	require.NoError(t, s.apiStore.SaveRun(ctx, original))
	command := controlplane.AgentCommand{ID: "old-command-" + suffix, AgentID: agentID, Type: "run_assigned", Run: &original, ActionID: original.ID, TopologyUID: st.TopologyUID, ExpectedSerial: st.Meta.Serial, Attempt: 1, LeaseOwner: "interrupted", LeaseUntil: time.Now().Add(10 * time.Minute), Status: controlplane.AgentCommandStatusLeased}
	require.NoError(t, s.apiStore.SaveAgentCommand(ctx, command))
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	session, err := authority.OpenSession(ctx, agentID, "original-process", "")
	require.NoError(t, err)
	grantSource, err := s.sourceGrant(ctx, agentID, ExecutionGrantRequest{SessionID: session.ID, Attempt: 1, LeaseOwner: "interrupted", BootID: oldBoot}, &persistence.CommandRecord{Command: command})
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, grantSource, time.Minute)
	require.NoError(t, err)
	external := ssh("sudo docker create --network none --name " + topology + " --label sysbox.managed=true --label sysbox.owner_uid=" + st.TopologyUID + " --label sysbox.topology_uid=" + st.TopologyUID + " --label sysbox.resource_uid=node --label sysbox.generation=1 alpine:3.22 sleep infinity")
	candidate := state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: external, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, Attributes: state.MustAttributes(map[string]any{"container_id": external})}
	providerRaw, err := json.Marshal(dockerprovider.HandleState{ContainerName: topology})
	require.NoError(t, err)
	require.NoError(t, candidate.SetProviderState(providerRaw))
	save("resource-binding.json", map[string]string{"external_id": external, "owner_uid": st.TopologyUID, "topology": topology})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("test-owned container evidence retained:", external)
			return
		}
		remaining := ssh("sudo docker ps -aq --no-trunc --filter id=" + external)
		if remaining != "" {
			t.Errorf("test-owned container remains: %s", remaining)
		}
	})
	action := runtime.ActionRecord{ID: "unknown-create", Topology: topology, RunID: original.ID, RunQueuedAt: original.QueuedAt, RunRevision: original.Revision, Resource: candidate.Address, Action: model.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1, ExpectedSerial: st.Meta.Serial, StateSerial: st.Meta.Serial, Candidate: &candidate, ReplayForbidden: true}
	require.NoError(t, repo.CommitActionIntent(ctx, action))
	status, data = request("POST", "/v1/topologies/"+topology+"/host-fences", map[string]any{"run_id": original.ID, "grant": grant}, "operator-fence")
	require.Equal(t, 202, status, string(data))
	var fence persistence.HostFenceRecord
	require.NoError(t, json.Unmarshal(data, &fence))
	admission := save("admission.json", fence)
	controllerPath := filepath.Join(round, "controller.key")
	require.NoError(t, os.WriteFile(controllerPath, controller, 0600))
	out, err := exec.CommandContext(ctx, "/usr/bin/python3", "../../scripts/acceptance/host_fencing.py", "fence", "--root", labRoot, "--admission", admission, "--attestor", binary, "--controller-key", controllerPath).CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.WriteFile(filepath.Join(round, "fence.log"), out, 0600))
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var location struct{ Evidence string }
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &location))
	raw, err = os.ReadFile(strings.TrimSuffix(location.Evidence, ".json") + ".receipt.json")
	require.NoError(t, err)
	var receipt controlplane.HostFenceReceipt
	require.NoError(t, json.Unmarshal(raw, &receipt))
	status, data = request("POST", "/v1/topologies/"+topology+"/host-fences/"+fence.Request.ID+"/receipt", receipt, "")
	require.Equal(t, 200, status, string(data))
	ssh("sudo mkdir -m 755 " + remote)
	upload(binary, "/var/tmp/sysbox-cli-"+suffix)
	ssh("sudo mv /var/tmp/sysbox-cli-" + suffix + " " + remote + "/sysbox && sudo chown root:root " + remote + "/sysbox && sudo chmod 755 " + remote + "/sysbox")
	for name, key := range map[string][]byte{"issuer.pub": issuerPub, "host.key": hostKey} {
		path := filepath.Join(round, name)
		require.NoError(t, os.WriteFile(path, key, 0600))
		upload(path, "/var/tmp/"+name+"-"+suffix)
		ssh("sudo mv /var/tmp/" + name + "-" + suffix + " " + remote + "/" + name)
	}
	hostCfg := config.DefaultServiceConfig()
	hostCfg.Paths.Home = remote + "/host"
	hostCfg.Paths.RunsDir = remote + "/host/runs"
	hostCfg.Paths.WorkspacesDir = remote + "/host/workspaces"
	hostCfg.Paths.Cache = remote + "/cache"
	hostCfg.State.Backend = strings.Replace(cfg.State.Backend, "127.0.0.1", "10.0.2.2", 1)
	hostCfg.Agent.ExecutionGrantPublicKeyFile = remote + "/issuer.pub"
	hostCfg.HostSupervisor = config.HostSupervisorConfig{Root: remote + "/journal", Socket: remote + "/host.sock", SigningKeyFile: remote + "/host.key", WorkerGroupID: 65534}
	workerCfg := hostCfg
	workerCfg.Paths.Home = remote + "/worker"
	workerCfg.Paths.RunsDir = remote + "/worker/runs"
	workerCfg.Paths.WorkspacesDir = remote + "/worker/workspaces"
	workerCfg.State.Backend = ""
	workerCfg.Agent.HostSupervisorSocket = hostCfg.HostSupervisor.Socket
	workerCfg.Agent.ExecutionSessionFile = remote + "/worker/session.json"
	identity := agent.Identity{ID: agentID, APIURL: guestURL, Token: serviceToken, Secret: secret, Capabilities: agent.DeriveCapabilities([]string{"docker"})}
	for name, value := range map[string]any{"host.json": hostCfg, "worker.json": workerCfg, "identity.json": identity} {
		path := save(name, value)
		upload(path, "/var/tmp/"+name+"-"+suffix)
		ssh("sudo mv /var/tmp/" + name + "-" + suffix + " " + remote + "/" + name)
	}
	ssh("sudo chown -R root:root " + remote + " && sudo chmod 600 " + remote + "/host.key " + remote + "/host.json " + remote + "/identity.json && sudo chmod 644 " + remote + "/issuer.pub " + remote + "/worker.json && sudo install -d -m 700 -o 65534 -g 65534 " + remote + "/worker && sudo install -m 600 -o 65534 -g 65534 " + remote + "/identity.json " + remote + "/worker/identity.json")
	sessionPath := save("session.json", map[string]any{"version": 1, "session": session})
	upload(sessionPath, "/var/tmp/operator-session-"+suffix)
	ssh("sudo install -m 600 -o 65534 -g 65534 /var/tmp/operator-session-" + suffix + " " + remote + "/worker/session.json && rm /var/tmp/operator-session-" + suffix)
	hostUnit, workerUnit := "sbo-host-"+suffix, "sbo-worker-"+suffix
	t.Cleanup(func() {
		ssh("sudo systemctl stop " + workerUnit + " " + hostUnit)
		for _, unit := range []string{workerUnit, hostUnit} {
			require.Equal(t, "0", ssh("sudo systemctl show "+unit+" --property=MainPID --value"), "service still running; preserve journal")
		}
		if t.Failed() {
			t.Log("failed operator journal retained:", remote)
			return
		}
		ssh("sudo rm -rf -- " + remote)
	})
	ssh("sudo systemd-run --unit=" + hostUnit + " --property=Environment=SYSBOX_CONFIG=" + remote + "/host.json " + remote + "/sysbox host-supervisor --config " + remote + "/host.json --identity " + remote + "/identity.json")
	ssh("sudo systemd-run --unit=" + workerUnit + " --uid=65534 --gid=65534 --property=Environment=SYSBOX_CONFIG=" + remote + "/worker.json " + remote + "/sysbox agent start --config " + remote + "/worker.json --identity " + remote + "/worker/identity.json --poll-interval 100ms")
	t.Cleanup(func() {
		for _, unit := range []string{hostUnit, workerUnit} {
			log := ssh("sudo journalctl -u " + unit + " --no-pager -n 100")
			_ = os.WriteFile(filepath.Join(round, unit+".log"), []byte(log), 0600)
		}
	})
	cli := func(operation string, extra ...string) []byte {
		t.Helper()
		args := append([]string{"host-fence", operation, fence.Request.ID, "--api", localURL, "--topology", topology}, extra...)
		c := exec.CommandContext(ctx, binary, args...)
		c.Env = append(os.Environ(), "SYSBOX_API_TOKEN="+token)
		out, err := c.CombinedOutput()
		require.NoError(t, err, string(out))
		return out
	}
	require.Eventually(t, func() bool {
		a, e := s.apiStore.GetAgent(ctx, agentID)
		return e == nil && a.LastHeartbeat.After(receipt.ObservedAt)
	}, 20*time.Second, 200*time.Millisecond)
	raw = cli("observe", "--key", "original-observation")
	var observed struct {
		CommandID string `json:"command_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &observed))
	var plan hostFenceRecoveryPlan
	require.Eventually(t, func() bool {
		code, data := request("GET", "/v1/topologies/"+topology+"/host-fences/"+fence.Request.ID+"/recovery-plan?observation="+observed.CommandID, nil, "")
		return code == 200 && json.Unmarshal(data, &plan) == nil && plan.ObservationAttestation == "verified"
	}, 30*time.Second, 200*time.Millisecond)
	require.NoError(t, json.Unmarshal(cli("recovery-plan", "--observation", observed.CommandID), &plan))
	require.Len(t, plan.Actions, 1)
	d := controlplane.IntentDispositionDecision{Version: 1, ID: "retain-original", FenceID: fence.Request.ID, Topology: topology, RunID: original.ID, RunQueuedAt: original.QueuedAt, RunRevision: original.Revision, ExpectedSerial: *plan.ExpectedSerial, JournalSHA256: plan.CurrentJournalSHA256, ObservationSHA256: plan.ObservationSHA256, Reason: "retain exact interrupted create for explicit destroy", Actions: []controlplane.IntentDispositionAction{{ActionID: action.ID, Decision: "retain-incomplete", ResourceUID: "node", Generation: 1, ExternalID: external}}}
	decision := save("decision.json", map[string]any{"decision": d, "observation_command_id": observed.CommandID})
	cli("reconcile-intents", "--decision", decision)
	cli("reconcile-intents", "--decision", decision)
	retained, err := repo.LoadState(ctx, topology)
	require.NoError(t, err)
	require.True(t, retained.FindResource(candidate.Address).RecoveryRequired())
	ended, err := s.apiStore.GetRun(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunFailed, ended.Status)
	require.Contains(t, ended.Err, "unknown")
	waitRun := func(id string) {
		t.Helper()
		require.Eventually(t, func() bool {
			r, e := s.apiStore.GetRun(ctx, id)
			if e == nil && r.Status == controlplane.RunFailed {
				t.Log(r.Err)
			}
			return e == nil && r.Status == controlplane.RunDone
		}, 60*time.Second, 200*time.Millisecond)
	}
	runRequest := func(op, key string, body any) string {
		t.Helper()
		status, data := request("POST", "/v1/topologies/"+topology+"/"+op, body, key)
		require.Equal(t, 202, status, string(data))
		var accepted struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(data, &accepted))
		return accepted.RunID
	}
	waitRun(runRequest("destroy", "explicit-destroy", nil))
	require.Equal(t, "absent", ssh("if sudo docker inspect "+external+" >/dev/null 2>&1; then echo present; else echo absent; fi"))
	cli("reconcile-intents", "--decision", decision)
	waitRun(runRequest("apply", "fresh-work", map[string]any{"revision": revision.Revision}))
	fresh, err := repo.LoadState(ctx, topology)
	require.NoError(t, err)
	require.NotNil(t, fresh.FindResource(candidate.Address))
	require.NotEqual(t, external, fresh.FindResource(candidate.Address).ExternalID)
	cli("reconcile-intents", "--decision", decision)
	unchanged, err := repo.LoadState(ctx, topology)
	require.NoError(t, err)
	require.Equal(t, fresh.Meta.Serial, unchanged.Meta.Serial)
	waitRun(runRequest("destroy", "final-destroy", nil))
	old, err := s.apiStore.GetRun(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.RunFailed, old.Status)
	require.Contains(t, old.Err, "unknown")
	save("result.json", map[string]any{"status": "passed", "old_run": old, "old_external_id": external, "new_external_id": fresh.FindResource(candidate.Address).ExternalID, "fence": receipt, "observation": plan, "original_unknown_preserved": true})
	t.Log("real host fence -> CLI observe/plan/disposition -> Agent Destroy -> new Apply -> old decision retry -> final Destroy; unknown history preserved")
}
