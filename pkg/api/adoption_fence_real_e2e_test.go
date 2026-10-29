//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// TestAdoptionFenceWholeVMCancellationMatrixE2E fixes every scenario explicitly;
// ambient provider/cancellation settings cannot silently select owned recovery.
func TestAdoptionFenceWholeVMCancellationMatrixE2E(t *testing.T) {
	for _, tc := range []struct{ provider, mode string }{
		{"libvirt", "unowned"},
		{"libvirt", "absent"},
		{"libvirt", "source"},
		{"firecracker", "source"},
		{"firecracker", "source-absent"},
	} {
		t.Run(tc.provider+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("SYSBOX_ADOPTION_FENCE_PROVIDER", tc.provider)
			t.Setenv("SYSBOX_ADOPTION_FENCE_CANCEL", tc.mode)
			TestAdoptionFenceWholeVMRecoveryE2E(t)
		})
	}
}

func TestAdoptionFenceWholeVMRecoveryE2E(t *testing.T) {
	root := os.Getenv("SYSBOX_GC_FENCE_LAB")
	if root == "" {
		t.Skip("requires newly scoped disposable whole-VM fencing lab")
	}
	var lab struct {
		ID    string         `json:"id"`
		Ports map[string]int `json:"ports"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "lab.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &lab))
	var scope struct {
		Disposable bool   `json:"disposable"`
		LabID      string `json:"lab_id"`
		Root       string `json:"root"`
	}
	raw, err = os.ReadFile(filepath.Join(root, "fencing-scope.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.True(t, scope.Disposable)
	require.Equal(t, lab.ID, scope.LabID)
	require.Equal(t, root, scope.Root)
	ssh := func(command string) string {
		t.Helper()
		cmd := exec.Command("ssh", "-F", "/dev/null", "-i", filepath.Join(root, "client"), "-p", fmt.Sprint(lab.Ports["execution"]), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(root, "known_hosts"), "p9@127.0.0.1", command)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	upload := func(local, remote string) {
		t.Helper()
		cmd := exec.Command("scp", "-F", "/dev/null", "-q", "-i", filepath.Join(root, "client"), "-P", fmt.Sprint(lab.Ports["execution"]), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(root, "known_hosts"), local, "p9@127.0.0.1:"+remote)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	save := func(path string, v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
	round := filepath.Join(root, "adoption-round-"+uuid.NewString())
	require.NoError(t, os.Mkdir(round, 0700))
	ctx := context.Background()
	s := NewServer(filepath.Join(round, "gc-api"), filepath.Join(round, "gc-workspaces"))
	require.NoError(t, s.initErr)
	server := httptest.NewServer(s)
	defer server.Close()
	issuerPub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	hostPub, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"gc-host": hostPub}
	controllerPub, controller, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	controllerPath := filepath.Join(round, "gc-controller.key")
	require.NoError(t, os.WriteFile(controllerPath, controller, 0600))
	secret := "isolated-gc-secret"
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "gc-host", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	repo, err := s.actionRepository("gc-lab")
	require.NoError(t, err)
	st, err := repo.LoadState(ctx, "gc-lab")
	require.NoError(t, err)
	id := uuid.NewString()
	cancelKind := os.Getenv("SYSBOX_ADOPTION_FENCE_CANCEL")
	providerKind := os.Getenv("SYSBOX_ADOPTION_FENCE_PROVIDER")
	if providerKind == "" {
		providerKind = "libvirt"
	}
	require.Contains(t, []string{"libvirt", "firecracker"}, providerKind)
	sourceOwned := cancelKind == "source" || cancelKind == "source-absent"
	absent := cancelKind == "absent" || cancelKind == "source-absent"
	var sourceIdentity substrate.OperationIdentity
	var sourceRepo *persistence.Repository
	var sourceState *state.State
	if sourceOwned {
		sourceRepo, err = s.actionRepository("source")
		require.NoError(t, err)
		sourceState, err = sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		sourceIdentity = substrate.OperationIdentity{ActionID: "original", OwnerUID: sourceState.TopologyUID, TopologyUID: sourceState.TopologyUID, ResourceUID: "source-resource", Generation: 1}
	}
	if providerKind == "libvirt" {
		definition, err := libvirtprovider.GenerateDomainXML(libvirtprovider.DomainSpec{Name: "sysbox-adoption-fence-" + id, UUID: id, VCPUs: 1, MemoryMiB: 128})
		require.NoError(t, err)
		if sourceOwned {
			name := "sysbox-adoption-fence-" + id
			hs := &libvirtprovider.HandleState{Identity: &sourceIdentity, DomainName: name, DomainUUID: id, ExternalDefinition: true, AdoptionFingerprint: "original", NetworkInit: substrate.GuestNetworkInitPreconfigured}
			definition, err = libvirtprovider.GenerateDomainXML(libvirtprovider.DomainSpec{Name: name, UUID: id, VCPUs: 1, MemoryMiB: 128, Identity: &sourceIdentity, ManagedState: hs})
			require.NoError(t, err)
		}
		definition = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(definition, "")
		xmlPath := filepath.Join(round, "domain.xml")
		require.NoError(t, os.WriteFile(xmlPath, []byte(definition), 0600))
		upload(xmlPath, "/var/tmp/adoption-domain.xml")
		ssh("sudo virsh -c qemu:///system define /var/tmp/adoption-domain.xml")
	}
	upload(os.Getenv("SYSBOX_GC_FENCE_PROBE_BINARY"), "/var/tmp/sysbox-adoption-probe.test")
	ssh("chmod 700 /var/tmp/sysbox-adoption-probe.test")
	adopt := controlplane.AdoptionRequest{ID: "adopt-fenced", Topology: "gc-lab", TopologyUID: st.TopologyUID, Address: "sysbox_node.adopted", Provider: providerKind, AgentID: "gc-host", ExternalID: id, ResourceUID: "adopted", Generation: 1, ExpectedSerial: st.Meta.Serial}
	ssh("sudo install -d -m 0700 /var/lib/sysbox/adoption-fence-lab")
	probe := map[string]any{"Identity": agent.Identity{ID: "gc-host", Secret: secret, APIURL: strings.Replace(server.URL, "127.0.0.1", "10.0.2.2", 1)}, "Issuer": issuerPub, "Host": hostKey, "Root": "/var/lib/sysbox/adoption-fence-lab/" + id, "Mode": "inspect", "Adoption": adopt, "Output": "/var/tmp/adoption-output.json"}
	if sourceOwned {
		probe["SourceIdentity"] = sourceIdentity
	}
	probePath := filepath.Join(round, "probe.json")
	runProbe := func() {
		save(probePath, probe)
		upload(probePath, "/var/tmp/adoption-probe.json")
		ssh("sudo env SYSBOX_ADOPTION_FENCE_PROBE=/var/tmp/adoption-probe.json /var/tmp/sysbox-adoption-probe.test -test.run=^TestAdoptionFenceGuestProbe$ -test.v")
	}
	if providerKind == "firecracker" {
		probe["Mode"] = "create-firecracker"
	}
	runProbe()
	require.NoError(t, json.Unmarshal([]byte(ssh("sudo cat /var/tmp/adoption-output.json")), &adopt.Evidence))
	if providerKind == "firecracker" {
		id = adopt.Evidence.ExternalID
		adopt.ExternalID = id
	}
	if sourceOwned {
		adopt.Generation = 2
		sourceState.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "original"), ResourceType: "sysbox_node", Driver: providerKind, ExternalID: id, OwnerUID: sourceIdentity.OwnerUID, ResourceUID: sourceIdentity.ResourceUID, Generation: 1}}
		manager := state.NewManagerWithBackend(sourceRepo.StateBackend("source"))
		require.NoError(t, manager.Save(sourceState))
		sourceState, err = manager.Load()
		require.NoError(t, err)
		target := substrate.OperationIdentity{ActionID: adopt.ID, OwnerUID: adopt.TopologyUID, TopologyUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: adopt.Generation}
		plan := controlplane.OwnershipTransferPlan{Version: 1, ID: adopt.ID, SourceTopology: "source", TargetTopology: adopt.Topology, SourceAddress: "sysbox_node.original", TargetAddress: adopt.Address, Source: sourceIdentity, Target: target, AgentID: adopt.AgentID, Provider: providerKind, ExternalID: id, SourceSerial: sourceState.Meta.Serial, TargetSerial: st.Meta.Serial, Fingerprint: adopt.Evidence.Fingerprint}
		// Source metadata and the target reservation are canonical. No provider
		// mutation is performed: the test interrupts transfer before its effect.
		transfer, err := s.prepareOwnershipTransfer(ctx, plan)
		require.NoError(t, err)
		adopt.Transfer = &transfer
	}
	op := prepareNodeOperation(controlplane.NodeOperation{ID: adopt.ID, Topology: adopt.Topology, Node: "adopted", AgentID: adopt.AgentID, Operation: "adoption_apply", ResourceUID: adopt.ResourceUID, Generation: adopt.Generation, Adoption: &adopt})
	op, err = (&NodeOperationService{operations: newNodeOperationStore(s.apiStore)}).enqueue(ctx, st.TopologyUID, op)
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, adopt.AgentID, op.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, adopt.AgentID, "process", "")
	require.NoError(t, err)
	oldBoot := ssh("cat /proc/sys/kernel/random/boot_id")
	grant, err := s.issueCommandGrant(ctx, adopt.AgentID, cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: oldBoot})
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuer, *grant, oldBoot, time.Now())
	require.NoError(t, err)
	probe["Adoption"] = adopt
	probe["Grant"] = signed
	probe["Mode"] = "unknown"
	if cancelKind != "" {
		require.Contains(t, []string{"unowned", "absent", "source", "source-absent"}, cancelKind)
		if providerKind == "firecracker" {
			require.True(t, sourceOwned)
		}
		probe["Mode"] = "unknown-cancel"
	}
	runProbe()
	if cancelKind != "" && !sourceOwned {
		old := runtime.ActionRecord{ID: "canonical-unknown", RunID: adopt.ID, Topology: adopt.Topology, Resource: address.Resource("sysbox_node", "adopted"), Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1, DesiredDigest: "old-intent", Candidate: &state.Resource{Address: address.Resource("sysbox_node", "adopted"), Driver: adopt.Provider, ExternalID: id, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: 1}}
		require.NoError(t, repo.CommitActionIntent(ctx, old))
	}
	digest, err := adopt.Digest()
	require.NoError(t, err)
	request := controlplane.HostFenceRequest{Version: 1, ID: "adoption-fence", Topology: adopt.Topology, RunID: adopt.ID, LabID: lab.ID, ControllerKey: controllerPub, JournalSHA256: digest, Grant: *grant}
	admission, err := authority.BeginHostFence(ctx, request)
	require.NoError(t, err)
	path := filepath.Join(round, "admission.json")
	save(path, admission)
	out, err := exec.Command("/usr/bin/python3", "../../scripts/acceptance/host_fencing.py", "fence", "--root", root, "--admission", path, "--attestor", os.Getenv("SYSBOX_GC_FENCE_ATTESTOR"), "--controller-key", controllerPath).CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.WriteFile(filepath.Join(round, "fencing.log"), out, 0600))
	paths, err := filepath.Glob(filepath.Join(root, "fence-*.receipt.json"))
	require.NoError(t, err)
	requestDigest, err := request.Digest()
	require.NoError(t, err)
	var receipt controlplane.HostFenceReceipt
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var r controlplane.HostFenceReceipt
		require.NoError(t, json.Unmarshal(raw, &r))
		if r.RequestSHA256 == requestDigest {
			receipt = r
		}
	}
	require.NotEmpty(t, receipt.Signature)
	require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
	probe["Mode"] = "recover"
	if cancelKind != "" {
		probe["Mode"] = "cancel"
	}
	if absent {
		if providerKind == "firecracker" {
			probe["Mode"] = "cleanup-source"
			runProbe()
			probe["Mode"] = "cancel"
		} else {
			ssh("sudo virsh -c qemu:///system undefine " + id)
		}
	}
	// Do not forge observation time to compensate for independent VM clocks.
	// Wait for the host clock to reach the signed controller receipt, bounded.
	if cancelKind != "" {
		require.Eventually(t, func() bool {
			current, err := time.Parse(time.RFC3339Nano, ssh("date -u +%Y-%m-%dT%H:%M:%S.%NZ"))
			return err == nil && !current.Before(receipt.ObservedAt)
		}, 10*time.Second, 100*time.Millisecond, "host clock has not reached fence receipt")
	}
	probe["FenceID"] = request.ID
	runProbe()
	var proof controlplane.AdoptionDispositionProof
	require.NoError(t, json.Unmarshal([]byte(ssh("sudo cat /var/tmp/adoption-output.json")), &proof))
	require.NoError(t, s.reconcileAdoptionFence(ctx, proof))
	require.NoError(t, s.reconcileAdoptionFence(ctx, proof))
	fenceAfter, err := authority.ReadHostFence(ctx, request.ID)
	require.NoError(t, err)
	require.NotNil(t, fenceAfter.AdoptionDisposition)
	require.False(t, fenceAfter.AdoptionDisposition.ReleasedAt.IsZero())
	require.Error(t, authority.Authorize(ctx, *grant), "old request cannot regain authority")
	if cancelKind != "" {
		expected := "cancel-" + cancelKind
		if absent {
			expected = "cancel-absent"
		}
		require.Equal(t, expected, proof.Disposition())
		refs, err := repo.LoadGCReferences(ctx, adopt.Topology)
		require.NoError(t, err)
		if !absent {
			protected := false
			for _, ref := range refs.References {
				if ref.ExternalID == id && ref.Driver == providerKind && strings.Contains(ref.Source, "adoption-cancel/") {
					protected = true
				}
			}
			require.True(t, protected, "retained object must remain protected from GC")
		}
	}
	recovered, err := repo.LoadState(ctx, adopt.Topology)
	require.NoError(t, err)
	if cancelKind == "" {
		require.Len(t, recovered.Resources, 1)
		require.Equal(t, id, recovered.Resources[0].ExternalID)
	} else {
		require.Empty(t, recovered.Resources)
		actions, err := repo.LoadActions(ctx, adopt.Topology)
		require.NoError(t, err)
		if sourceOwned {
			require.Empty(t, actions)
			actions = []runtime.ActionRecord{{ID: "placeholder", Topology: adopt.Topology, Resource: address.Resource("sysbox_node", "adopted"), Action: model.PlanActionAdopt, Status: runtime.ActionIntentCommitted, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: adopt.Generation, DesiredDigest: "fresh", Candidate: &state.Resource{Address: address.Resource("sysbox_node", "adopted"), Driver: adopt.Provider, ExternalID: id, OwnerUID: adopt.TopologyUID, ResourceUID: adopt.ResourceUID, Generation: adopt.Generation}}}
			barrier, err := authority.InspectMaintenance(ctx, adopt.ID)
			require.NoError(t, err)
			require.True(t, barrier.Released)
			sourceAfter, err := sourceRepo.LoadState(ctx, "source")
			require.NoError(t, err)
			require.Empty(t, sourceAfter.Resources, "source retirement cannot be undone by cancellation")
		} else {
			require.Len(t, actions, 1)
			require.NotEmpty(t, actions[0].DispositionID)
		}
		require.Equal(t, runtime.ActionIntentCommitted, actions[0].Status)
		next := actions[0]
		next.ID = "fresh-action"
		next.RunID = "fresh-run"
		next.DispositionID = ""
		next.Revision = 0
		next.ExpectedSerial = recovered.Meta.Serial
		next.StateSerial = recovered.Meta.Serial
		require.NoError(t, repo.CommitActionIntent(ctx, next))
	}
	ended, err := s.apiStore.GetNodeOperation(ctx, adopt.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.NodeOperationStatusFailed, ended.Status)
	require.Contains(t, ended.Err, "original result unknown")
	if providerKind == "libvirt" && !absent {
		require.Contains(t, ssh("sudo virsh -c qemu:///system list --all --uuid"), id)
	}
	save(filepath.Join(round, "result.json"), map[string]any{"proof": proof, "state": recovered, "operation": ended, "old_boot": oldBoot, "new_boot": receipt.NewBootID})
	if providerKind == "libvirt" {
		if !absent {
			ssh("sudo virsh -c qemu:///system undefine " + id)
		}
	} else if !absent {
		if sourceOwned {
			probe["Mode"] = "cleanup-source"
		} else {
			probe["Mode"] = "verify-restart-cleanup"
		}
		runProbe()
	}
}
