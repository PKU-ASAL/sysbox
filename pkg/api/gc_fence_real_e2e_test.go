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
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestGCFenceWholeVMDispositionE2E(t *testing.T) {
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
	round := filepath.Join(root, "gc-round-"+uuid.NewString())
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
	refs, err := repo.LoadGCReferences(ctx, "gc-lab")
	require.NoError(t, err)
	candidate := controlplane.GCCandidate{Kind: "domain", ExternalID: uuid.NewString(), TopologyUID: refs.TopologyUID, OwnerUID: refs.TopologyUID, ResourceUID: "orphan", Generation: 1, InventoryDigest: refs.Digest}
	providerKind := os.Getenv("SYSBOX_GC_FENCE_PROVIDER")
	if providerKind == "" {
		providerKind = "domain"
	}
	candidate.Kind = providerKind
	journalRoot := "/var/tmp/gcj-" + candidate.ExternalID
	upload(os.Getenv("SYSBOX_GC_FENCE_PROBE_BINARY"), "/var/tmp/sysbox-gc-probe.test")
	ssh("chmod 700 /var/tmp/sysbox-gc-probe.test")
	if providerKind == "firecracker" {
		createInput := map[string]any{"mode": "create-firecracker", "candidate": candidate, "root": journalRoot, "kernel": "/var/tmp/gc-vmlinux", "rootfs": "/var/tmp/gc-rootfs.ext4", "binary": "/var/tmp/gc-firecracker", "output": "/var/tmp/gc-created.json"}
		path := filepath.Join(round, "gc-create.json")
		save(path, createInput)
		upload(path, "/var/tmp/sysbox-gc-probe.json")
		ssh("sudo env SYSBOX_GC_FENCE_PROBE=/var/tmp/sysbox-gc-probe.json /var/tmp/sysbox-gc-probe.test -test.run=^TestGCFenceGuestProbe$ -test.v")
		var created struct {
			ID string `json:"external_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(ssh("sudo cat /var/tmp/gc-created.json")), &created))
		require.NotEmpty(t, created.ID)
		candidate.ExternalID = created.ID
	} else {
		op := substrate.OperationIdentity{ActionID: "create", TopologyUID: candidate.TopologyUID, OwnerUID: candidate.OwnerUID, ResourceUID: candidate.ResourceUID, Generation: 1}
		xml, err := libvirtprovider.GenerateDomainXML(libvirtprovider.DomainSpec{Name: "sysbox-gc-fence-" + candidate.ExternalID, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", UUID: candidate.ExternalID, Identity: &op, ManagedState: &libvirtprovider.HandleState{Identity: &op, DomainName: "sysbox-gc-fence-" + candidate.ExternalID, DomainUUID: candidate.ExternalID}})
		require.NoError(t, err)
		xml = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(xml, "")
		xmlPath := filepath.Join(round, "gc-target.xml")
		require.NoError(t, os.WriteFile(xmlPath, []byte(xml), 0600))
		upload(xmlPath, "/var/tmp/gc-target.xml")
		ssh("sudo virsh -c qemu:///system define /var/tmp/gc-target.xml")
	}
	reservation := persistence.GCReservation{ID: "gc-fencing-real", AgentID: "gc-host", Candidate: candidate}
	require.NoError(t, s.reserveGCWithAuthority(ctx, "gc-lab", reservation))
	require.NoError(t, s.publishGCReservation(ctx, "gc-lab", reservation))
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "gc-host", "process", "")
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "gc-host", reservation.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	oldBoot := ssh("cat /proc/sys/kernel/random/boot_id")
	grant, err := s.issueCommandGrant(ctx, "gc-host", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: oldBoot})
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuer, *grant, oldBoot, time.Now())
	require.NoError(t, err)
	request := controlplane.HostFenceRequest{Version: 1, ID: "gcf-real", Topology: "gc-lab", RunID: reservation.ID, LabID: lab.ID, ControllerKey: controllerPub, JournalSHA256: gcEvidenceHash(reservation), Grant: *grant}
	probe := map[string]any{"identity": agent.Identity{ID: "gc-host", Secret: secret, APIURL: strings.Replace(server.URL, "127.0.0.1", "10.0.2.2", 1)}, "issuer_key": issuerPub, "host_key": hostKey, "root": journalRoot, "candidate": candidate, "grant": signed, "fence_id": request.ID, "mode": "intent", "output": "/var/tmp/sysbox-gc-proof.json"}
	probe["kernel"] = "/var/tmp/gc-vmlinux"
	probe["rootfs"] = "/var/tmp/gc-rootfs.ext4"
	probe["binary"] = "/var/tmp/gc-firecracker"
	probePath := filepath.Join(round, "gc-probe.json")
	save(probePath, probe)
	upload(os.Getenv("SYSBOX_GC_FENCE_PROBE_BINARY"), "/var/tmp/sysbox-gc-probe.test")
	upload(probePath, "/var/tmp/sysbox-gc-probe.json")
	ssh("chmod 700 /var/tmp/sysbox-gc-probe.test; sudo env SYSBOX_GC_FENCE_PROBE=/var/tmp/sysbox-gc-probe.json /var/tmp/sysbox-gc-probe.test -test.run=^TestGCFenceGuestProbe$ -test.v")
	admission, err := authority.BeginHostFence(ctx, request)
	require.NoError(t, err)
	admissionPath := filepath.Join(round, "gc-admission.json")
	save(admissionPath, admission)
	fenceCmd := exec.Command("/usr/bin/python3", "../../scripts/acceptance/host_fencing.py", "fence", "--root", root, "--admission", admissionPath, "--attestor", os.Getenv("SYSBOX_GC_FENCE_ATTESTOR"), "--controller-key", controllerPath)
	out, err := fenceCmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.WriteFile(filepath.Join(round, "gc-fencing.log"), out, 0600))
	receipts, err := filepath.Glob(filepath.Join(root, "fence-*.receipt.json"))
	require.NoError(t, err)
	var receipt controlplane.HostFenceReceipt
	requestDigest, _ := request.Digest()
	found := false
	for _, path := range receipts {
		raw, err = os.ReadFile(path)
		require.NoError(t, err)
		var candidateReceipt controlplane.HostFenceReceipt
		require.NoError(t, json.Unmarshal(raw, &candidateReceipt))
		if candidateReceipt.RequestSHA256 == requestDigest {
			receipt = candidateReceipt
			found = true
		}
	}
	require.True(t, found)
	require.NoError(t, authority.RecordHostFence(ctx, request.ID, receipt))
	probe["mode"] = "corrupt"
	save(probePath, probe)
	upload(probePath, "/var/tmp/sysbox-gc-probe.json")
	ssh("sudo env SYSBOX_GC_FENCE_PROBE=/var/tmp/sysbox-gc-probe.json /var/tmp/sysbox-gc-probe.test -test.run=^TestGCFenceGuestProbe$ -test.v")
	var proof controlplane.GCDispositionProof
	require.NoError(t, json.Unmarshal([]byte(ssh("sudo cat /var/tmp/sysbox-gc-proof.json")), &proof))
	require.Equal(t, "present", proof.Presence)
	require.NoError(t, s.reconcileGCFence(ctx, proof))
	require.NoError(t, s.reconcileGCFence(ctx, proof))
	stored, err := repo.LoadGCReservation(ctx, "gc-lab", reservation.ID)
	require.NoError(t, err)
	require.False(t, stored.Pending())
	require.Empty(t, stored.ReceiptHash)
	if providerKind == "domain" {
		require.Contains(t, ssh("sudo virsh -c qemu:///system list --all --uuid"), candidate.ExternalID)
	}
	save(filepath.Join(round, "gc-disposition-result.json"), map[string]any{"proof": proof, "reservation": stored, "old_boot": oldBoot, "new_boot": receipt.NewBootID, "operation_result": "unknown", "provider_target_preserved": true})
	if providerKind == "domain" {
		ssh("sudo virsh -c qemu:///system undefine " + candidate.ExternalID)
	} else {
		// The disposed operation remains unknown and the manifest is retained.
		// Normal deletion correctly refuses an old-boot process anchor; later
		// fenced residue cleanup is a distinct explicitly authorized operation.
		t.Log("Firecracker manifest/disk retained in disposable VM after fenced disposition")
	}
}
