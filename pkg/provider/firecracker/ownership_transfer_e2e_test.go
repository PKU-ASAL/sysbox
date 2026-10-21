//go:build e2e

package firecracker

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
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerOwnershipTransferColdLifecycleE2E(t *testing.T) {
	binary, kernel, rootfs := os.Getenv("SYSBOX_FIRECRACKER_BIN"), os.Getenv("SYSBOX_FIRECRACKER_KERNEL"), os.Getenv("SYSBOX_FIRECRACKER_ROOTFS")
	if binary == "" || kernel == "" || rootfs == "" {
		t.Skip("requires isolated Firecracker artifacts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := config.DefaultServiceConfig()
	cfg.Providers.Firecracker.Binary = binary
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	t.Setenv("SYSBOX_CONFIG", path)
	s := New(kernel, t.TempDir())
	old := substrate.OperationIdentity{ActionID: "create", OwnerUID: "source", TopologyUID: "source", ResourceUID: "old", Generation: 1}
	scopedOld := substrate.WithOperationIdentity(ctx, old)
	h, err := s.CreateNode(scopedOld, substrate.NodeSpec{Name: "transfer", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	// Transfer the prepared generation before first start: config paths and disks
	// remain intact while ownership changes; target then exercises real KVM start.
	_, original, err := s.findAdoptionManifest(ctx, h.ID)
	require.NoError(t, err)
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}
	p := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "firecracker", ExternalID: h.ID, Source: old, Target: next, Fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256(original))}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := p.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: p, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("a", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, s.InspectOwnershipTransfer(ctx, p))
	scoped := substrate.WithOperationIdentity(ctx, next)
	inputPath := filepath.Join(t.TempDir(), "child.json")
	childRaw, _ := json.Marshal(fcTransferChildInput{Root: s.rootfsDir, Kernel: kernel, Proof: proof, Key: pub})
	require.NoError(t, os.WriteFile(inputPath, childRaw, 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestFirecrackerTransferCrashChild$")
	child.Env = append(os.Environ(), "SYSBOX_FC_TRANSFER_CHILD="+inputPath)
	output, childErr := child.CombinedOutput()
	var exited *exec.ExitError
	require.ErrorAs(t, childErr, &exited, string(output))
	require.Equal(t, 77, exited.ExitCode(), string(output))
	transferred, err := s.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.DestroyNode(substrate.WithOperationIdentity(cleanup, next), transferred)
	})
	require.Error(t, s.DestroyNode(scopedOld, h))
	cold := New(kernel, s.rootfsDir)
	found, err := cold.DiscoverManagedNode(scoped, next, "new")
	require.NoError(t, err)
	require.NoError(t, cold.StartNode(scoped, found))
	observed, err := cold.ObserveNode(scoped, found)
	require.NoError(t, err)
	require.True(t, observed.Exists)
	require.Greater(t, observed.PID, 0)
	require.NoError(t, cold.DestroyNode(scoped, found))
	require.NoDirExists(t, transferred.Provider.(*HandleState).VMDir)
}

type fcTransferChildInput struct {
	Root, Kernel string
	Proof        controlplane.OwnershipRetirementProof
	Key          ed25519.PublicKey
}

func TestFirecrackerTransferCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_FC_TRANSFER_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input fcTransferChildInput
	require.NoError(t, json.Unmarshal(raw, &input))
	_, err = New(input.Kernel, input.Root).ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), input.Proof.Plan.Target), input.Proof, input.Key)
	require.NoError(t, err)
	os.Exit(77)
}
