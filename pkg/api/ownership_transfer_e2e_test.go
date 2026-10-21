//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestLibvirtOwnershipTransferAPIHostCommitE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("requires disposable libvirt session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := NewServer(t.TempDir(), t.TempDir())
	server := httptest.NewServer(s)
	defer server.Close()
	issuerPub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
	host, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
	secret := "transfer-test-secret"
	require.NoError(t, s.apiStore.SaveAgent(ctx, controlplane.Agent{ID: "host", AuthSecret: secret, SecretHash: agent.SecretHash(secret)}))
	sourceRepo, err := s.actionRepository("source")
	require.NoError(t, err)
	targetRepo, err := s.actionRepository("target")
	require.NoError(t, err)
	manager := state.NewManagerWithBackend(sourceRepo.StateBackend("source"))
	source, err := manager.Load()
	require.NoError(t, err)
	target, err := targetRepo.LoadState(ctx, "target")
	require.NoError(t, err)
	old := substrate.OperationIdentity{ActionID: "create", OwnerUID: source.TopologyUID, TopologyUID: source.TopologyUID, ResourceUID: "old", Generation: 1}
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: target.TopologyUID, TopologyUID: target.TopologyUID, ResourceUID: "new", Generation: 2}
	id := uuid.NewString()
	name := "sysbox-transfer-api-" + id
	provider := libvirtprovider.New(t.TempDir())
	hs := &libvirtprovider.HandleState{Identity: &old, DomainName: name, DomainUUID: id, ExternalDefinition: true, AdoptionFingerprint: "original", NetworkInit: substrate.GuestNetworkInitPreconfigured}
	definition, err := libvirtprovider.GenerateDomainXML(libvirtprovider.DomainSpec{Name: name, UUID: id, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &old, ManagedState: hs})
	require.NoError(t, err)
	definition = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(definition, "")
	xmlPath := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(definition), 0600))
	output, err := exec.CommandContext(ctx, "virsh", "define", xmlPath).CombinedOutput()
	require.NoError(t, err, string(output))
	t.Cleanup(func() { _, _ = exec.Command("virsh", "undefine", id).CombinedOutput() })
	raw, err := exec.CommandContext(ctx, "virsh", "dumpxml", id).Output()
	require.NoError(t, err)
	source.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "old"), ResourceType: "sysbox_node", Driver: "libvirt", ExternalID: id, OwnerUID: old.OwnerUID, ResourceUID: old.ResourceUID, Generation: 1}}
	require.NoError(t, manager.Save(source))
	source, err = manager.Load()
	require.NoError(t, err)
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: id, Source: old, Target: next, SourceSerial: source.Meta.Serial, TargetSerial: target.Meta.Serial, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}
	require.NoError(t, targetRepo.ReserveOwnershipTarget(ctx, plan))
	barrier, err := s.beginOwnershipMaintenance(ctx, plan)
	require.NoError(t, err)
	authorityClient := hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "host", Secret: secret, APIURL: server.URL}}
	preparation, err := hostsupervisor.PrepareOwnership(ctx, plan, "host", "boot", hostKey, authorityClient.AuthorizeOwnershipPreparation, provider.InspectOwnershipTransfer)
	require.NoError(t, err)
	require.Equal(t, barrier.Epoch, preparation.SourceEpoch)
	require.NoError(t, targetRepo.RecordOwnershipPreparation(ctx, preparation, host))
	proof, err := s.prepareOwnershipTransfer(ctx, plan)
	require.NoError(t, err)
	req := controlplane.AdoptionRequest{Transfer: &proof, ID: plan.ID, Topology: "target", TopologyUID: next.TopologyUID, Address: plan.TargetAddress, Provider: "libvirt", AgentID: "host", ExternalID: id, ResourceUID: next.ResourceUID, Generation: 2, ExpectedSerial: plan.TargetSerial, Evidence: substrate.AdoptionEvidence{Provider: "libvirt", ExternalID: id, Fingerprint: plan.Fingerprint}}
	op := prepareNodeOperation(controlplane.NodeOperation{ID: plan.ID, Topology: "target", Node: "new", AgentID: "host", Operation: "adoption_apply", ResourceUID: next.ResourceUID, Generation: 2, Adoption: &req})
	op, err = (&NodeOperationService{operations: newNodeOperationStore(s.apiStore)}).enqueue(ctx, next.TopologyUID, op)
	require.NoError(t, err)
	cmd, ok, err := s.apiStore.AcquireAgentCommandLease(ctx, "host", op.CommandID, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	session, err := authority.OpenSession(ctx, "host", "worker", "")
	require.NoError(t, err)
	grant, err := s.issueCommandGrant(ctx, "host", cmd.ID, ExecutionGrantRequest{SessionID: session.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, BootID: "boot"})
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(issuer, *grant, "boot", time.Now())
	require.NoError(t, err)
	coordinator := hostsupervisor.AdoptionCoordinator{Root: t.TempDir(), IssuerKey: issuerPub, HostKey: hostKey, AgentID: "host", HostBootID: "boot", Authorize: authorityClient.Authorize, Report: authorityClient.ReportNodeOperation}
	_, err = coordinator.Apply(ctx, req, signed, func(ctx context.Context) (json.RawMessage, error) {
		h, err := provider.ApplyOwnershipTransfer(substrate.WithOperationIdentity(ctx, next), proof, issuerPub)
		if err != nil {
			return nil, err
		}
		return provider.MarshalProviderState(h)
	})
	require.NoError(t, err)
	oldState, err := sourceRepo.LoadState(ctx, "source")
	require.NoError(t, err)
	require.Empty(t, oldState.Resources)
	newState, err := targetRepo.LoadState(ctx, "target")
	require.NoError(t, err)
	require.Len(t, newState.Resources, 1)
	require.Equal(t, id, newState.Resources[0].ExternalID)
	barrier, err = authority.InspectMaintenance(ctx, plan.ID)
	require.NoError(t, err)
	require.True(t, barrier.Released)
	require.Error(t, authority.Authorize(ctx, *grant))
}
