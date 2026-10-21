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

	"errors"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
)

func TestLibvirtOwnershipTransferAPIHostCommitE2E(t *testing.T) {
	testLibvirtTransferHostBoundary(t, "normal")
}

func TestLibvirtOwnershipTransferHostSIGKILLMatrixE2E(t *testing.T) {
	for _, phase := range []string{"before-effect", "after-ownership", "after-observe", "before-report", "after-state", "after-report"} {
		t.Run(phase, func(t *testing.T) { testLibvirtTransferHostBoundary(t, phase) })
	}
}

func testLibvirtTransferHostBoundary(t *testing.T, phase string) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("requires disposable libvirt session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := NewServer(t.TempDir(), t.TempDir())
	var commitState func(json.RawMessage) error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-state-boundary" && commitState != nil {
			var evidence json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&evidence); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := commitState(evidence); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			w.WriteHeader(200)
			return
		}
		s.ServeHTTP(w, r)
	}))
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
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cleanup, "virsh", "undefine", id).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup disposable domain %s: %v: %s", id, err, out)
		}
	})
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
	commitState = func(evidence json.RawMessage) error { return s.commitAdoptionState(ctx, op, evidence) }
	if phase == "normal" {
		_, err = coordinator.Apply(ctx, req, signed, func(ctx context.Context) (json.RawMessage, error) {
			h, err := provider.ApplyOwnershipTransfer(substrate.WithOperationIdentity(ctx, next), proof, issuerPub)
			if err != nil {
				return nil, err
			}
			return provider.MarshalProviderState(h)
		})
		require.NoError(t, err)
	} else {
		input := transferHostCrashInput{Phase: phase, Root: coordinator.Root, ProviderRoot: t.TempDir(), Request: req, Grant: signed, Issuer: issuerPub, Host: hostKey, Identity: authorityClient.Identity, Ready: filepath.Join(t.TempDir(), "ready")}
		inputPath := filepath.Join(t.TempDir(), "input.json")
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(inputPath, raw, 0600))
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnershipTransferHostSIGKILLChild$")
		child.Env = append(os.Environ(), "SYSBOX_TRANSFER_HOST_CRASH="+inputPath)
		logFile, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
		require.NoError(t, err)
		defer logFile.Close()
		child.Stdout, child.Stderr = logFile, logFile
		require.NoError(t, child.Start())
		defer child.Process.Kill()
		require.Eventually(t, func() bool { _, err := os.Stat(input.Ready); return err == nil }, 20*time.Second, 20*time.Millisecond, "child boundary log: %s", logFile.Name())
		require.NoError(t, child.Process.Kill())
		var exited *exec.ExitError
		require.ErrorAs(t, child.Wait(), &exited)
		require.Equal(t, -1, exited.ExitCode(), "must be killed by OS signal")
		before, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		if phase == "after-state" || phase == "after-report" {
			require.Len(t, before.Resources, 1)
		} else {
			require.Empty(t, before.Resources)
		}
		providerBefore, err := provider.InspectAdoption(ctx, id, substrate.NodeSpec{})
		require.NoError(t, err)
		expectedOwner := next
		if phase == "before-effect" {
			expectedOwner = old
		}
		require.Equal(t, &expectedOwner, providerBefore.ManagedIdentity)
		require.NoError(t, coordinator.Recover(ctx))
		_, err = coordinator.Observe(ctx, req, signed)
		if phase == "before-effect" || phase == "after-ownership" || phase == "after-observe" {
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			_, err = coordinator.Apply(ctx, req, signed, func(context.Context) (json.RawMessage, error) {
				t.Error("uncertain provider call replayed")
				return nil, nil
			})
			require.Error(t, err)
			held, err := authority.InspectMaintenance(ctx, plan.ID)
			require.NoError(t, err)
			require.False(t, held.Released)
			unchanged, err := targetRepo.LoadState(ctx, "target")
			require.NoError(t, err)
			require.Empty(t, unchanged.Resources)
			providerAfter, err := provider.InspectAdoption(ctx, id, substrate.NodeSpec{})
			require.NoError(t, err)
			require.Equal(t, providerBefore, providerAfter, "unknown recovery cannot mutate provider ownership")
			return
		}
		require.NoError(t, err)
		recovered, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		require.Equal(t, target.Meta.Serial+1, recovered.Meta.Serial)
		if phase == "after-state" || phase == "after-report" {
			require.Equal(t, before.Meta.Serial, recovered.Meta.Serial)
		}
		// Repeated cold reports preserve the exact serial and cannot replay the provider.
		_, err = coordinator.Observe(ctx, req, signed)
		require.NoError(t, err)
		replayed, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		require.Equal(t, recovered.Meta.Serial, replayed.Meta.Serial)
	}
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

// This matrix kills the host process. The API remains live; API-process crash
// acceptance is separate and must not be inferred from these cases.
type transferHostCrashInput struct {
	Phase, Root, ProviderRoot, Ready string
	Request                          controlplane.AdoptionRequest
	Grant                            controlplane.SignedExecutionGrant
	Issuer                           ed25519.PublicKey
	Host                             ed25519.PrivateKey
	Identity                         agent.Identity
}

func TestOwnershipTransferHostSIGKILLChild(t *testing.T) {
	path := os.Getenv("SYSBOX_TRANSFER_HOST_CRASH")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var in transferHostCrashInput
	require.NoError(t, json.Unmarshal(raw, &in))
	boundary := func(phase string) {
		if phase != in.Phase {
			return
		}
		require.NoError(t, os.WriteFile(in.Ready, []byte(phase), 0600))
		select {}
	}
	ctx := context.Background()
	client := hostsupervisor.AuthorityClient{Identity: in.Identity}
	provider := libvirtprovider.New(in.ProviderRoot)
	coordinator := hostsupervisor.AdoptionCoordinator{Root: in.Root, IssuerKey: in.Issuer, HostKey: in.Host, AgentID: in.Request.AgentID, HostBootID: in.Grant.BootID, Authorize: client.Authorize}
	coordinator.Report = func(ctx context.Context, id string, r controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		boundary("before-report")
		if in.Phase == "after-state" {
			data, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			request, err := http.NewRequestWithContext(ctx, "POST", in.Identity.APIURL+"/test-state-boundary", strings.NewReader(string(data)))
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				return errors.New("state boundary commit failed")
			}
			boundary("after-state")
		}
		if err := client.ReportNodeOperation(ctx, id, r, evidence); err != nil {
			return err
		}
		boundary("after-report")
		return nil
	}
	_, err = coordinator.Apply(ctx, in.Request, in.Grant, func(ctx context.Context) (json.RawMessage, error) {
		boundary("before-effect")
		scoped := substrate.WithOperationIdentity(ctx, in.Request.Transfer.Plan.Target)
		h, err := provider.ApplyOwnershipTransfer(scoped, *in.Request.Transfer, in.Issuer)
		if err != nil {
			return nil, err
		}
		boundary("after-ownership")
		if _, err = provider.ObserveNode(scoped, h); err != nil {
			return nil, err
		}
		boundary("after-observe")
		return provider.MarshalProviderState(h)
	})
	require.NoError(t, err)
	t.Fatal("child returned without reaching requested boundary")
}
