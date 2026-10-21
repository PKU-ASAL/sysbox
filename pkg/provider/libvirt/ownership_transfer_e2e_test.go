//go:build e2e

package libvirt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestLibvirtOwnershipTransferColdReadbackE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("requires disposable libvirt session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider := New(t.TempDir())
	id := uuid.NewString()
	name := "sysbox-transfer-" + id
	old := substrate.OperationIdentity{ActionID: "original", OwnerUID: "source", TopologyUID: "source", ResourceUID: "old", Generation: 1}
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}
	hs := &HandleState{Identity: &old, DomainName: name, DomainUUID: id, ExternalDefinition: true, AdoptionFingerprint: "original", NetworkInit: substrate.GuestNetworkInitPreconfigured}
	definition, err := GenerateDomainXML(DomainSpec{Name: name, UUID: id, VCPUs: 1, MemoryMiB: 128, MachineType: "q35", Identity: &old, ManagedState: hs})
	require.NoError(t, err)
	definition = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(definition, "")
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(definition), 0600))
	out, err := exec.CommandContext(ctx, "virsh", "define", path).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		_, _ = exec.Command("virsh", "destroy", id).CombinedOutput()
		_, _ = exec.Command("virsh", "undefine", id).CombinedOutput()
	})
	raw, err := virshQuery(ctx, "dumpxml", id)
	require.NoError(t, err)
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: id, Source: old, Target: next, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := plan.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("a", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	cancelRequest := controlplane.AdoptionRequest{ExternalID: id, Transfer: &proof}
	sourceObservation, cancelErr := provider.InspectAdoptionCancellation(ctx, cancelRequest)
	require.NoError(t, cancelErr)
	resolution, cancelErr := controlplane.AdoptionCancellationResolution(cancelRequest, sourceObservation)
	require.NoError(t, cancelErr)
	require.Equal(t, "cancel-source", resolution)
	scoped := substrate.WithOperationIdentity(ctx, next)
	inputPath := filepath.Join(t.TempDir(), "child.json")
	childRaw, _ := json.Marshal(libvirtTransferChildInput{Root: provider.vmDirRoot(), Proof: proof, Key: pub})
	require.NoError(t, os.WriteFile(inputPath, childRaw, 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestLibvirtTransferCrashChild$")
	child.Env = append(os.Environ(), "SYSBOX_LIBVIRT_TRANSFER_CHILD="+inputPath)
	output, childErr := child.CombinedOutput()
	var exited *exec.ExitError
	require.ErrorAs(t, childErr, &exited, string(output))
	require.Equal(t, 77, exited.ExitCode(), string(output))
	recovered, recoveryErr := provider.InspectAdoptionRecovery(scoped, controlplane.AdoptionRequest{ExternalID: id})
	require.NoError(t, recoveryErr, "read-only ownership recovery after lost provider return")
	require.Equal(t, id, recovered.ID)
	require.Equal(t, plan.Target.Generation, recovered.Generation)
	h, err := provider.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	require.Equal(t, id, h.ID)
	cold := New(provider.vmDirRoot())
	again, err := cold.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	require.Equal(t, h.ID, again.ID)
	observed, err := cold.ObserveNode(scoped, again)
	require.NoError(t, err)
	require.True(t, observed.Exists)
	_, err = cold.ObserveNode(substrate.WithOperationIdentity(ctx, old), substrate.NodeHandle{ID: id, Generation: 1, Provider: hs})
	require.Error(t, err)
	require.NoError(t, cold.DestroyNode(scoped, again))
	present, err := gcDomainPresent(ctx, id)
	require.NoError(t, err)
	require.False(t, present)
}

type libvirtTransferChildInput struct {
	Root  string
	Proof controlplane.OwnershipRetirementProof
	Key   ed25519.PublicKey
}

func TestLibvirtTransferCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_LIBVIRT_TRANSFER_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input libvirtTransferChildInput
	require.NoError(t, json.Unmarshal(raw, &input))
	_, err = New(input.Root).ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), input.Proof.Plan.Target), input.Proof, input.Key)
	require.NoError(t, err)
	os.Exit(77)
}
