//go:build e2e

package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDockerOwnershipTransferColdLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_DOCKER_TRANSFER_E2E") != "1" {
		t.Skip("requires explicitly authorized disposable container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	s.adoptionRoot = t.TempDir()
	old := substrate.OperationIdentity{ActionID: "create", OwnerUID: "source-" + uuid.NewString(), ResourceUID: "old", Generation: 1}
	old.TopologyUID = old.OwnerUID
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target-" + uuid.NewString(), ResourceUID: "new", Generation: 2}
	next.TopologyUID = next.OwnerUID
	created, err := s.cli.ContainerCreate(ctx, &container.Config{Image: "alpine:3.22", Cmd: []string{"sleep", "60"}, Labels: operationLabels(old)}, &container.HostConfig{NetworkMode: "none"}, nil, nil, "sysbox-transfer-"+uuid.NewString())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
	evidence, err := s.InspectAdoption(ctx, created.ID, substrate.NodeSpec{})
	require.NoError(t, err)
	p := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", Provider: "docker", AgentID: "host", ExternalID: created.ID, Source: old, Target: next, Fingerprint: evidence.Fingerprint}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := p.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: p, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("a", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	scoped := substrate.WithOperationIdentity(ctx, next)
	require.NoError(t, s.InspectOwnershipTransfer(ctx, p))
	inputPath := filepath.Join(t.TempDir(), "child.json")
	childRaw, _ := json.Marshal(dockerTransferChildInput{Root: s.adoptionRoot, Proof: proof, Key: pub})
	require.NoError(t, os.WriteFile(inputPath, childRaw, 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestDockerTransferCrashChild$")
	child.Env = append(os.Environ(), "SYSBOX_DOCKER_TRANSFER_CHILD="+inputPath)
	output, childErr := child.CombinedOutput()
	var exited *exec.ExitError
	require.ErrorAs(t, childErr, &exited, string(output))
	require.Equal(t, 77, exited.ExitCode(), string(output))
	h, err := s.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	cold := &Substrate{cli: s.cli, adoptionRoot: s.adoptionRoot}
	_, err = cold.ApplyOwnershipTransfer(scoped, proof, pub)
	require.NoError(t, err)
	oldHandle := substrate.NodeHandle{ID: created.ID, Generation: 1}
	require.Error(t, cold.DestroyNode(substrate.WithOperationIdentity(ctx, old), oldHandle))
	require.NoError(t, cold.StartNode(scoped, h))
	observed, err := cold.ObserveNode(scoped, h)
	require.NoError(t, err)
	require.True(t, observed.Running)
	require.NoError(t, cold.DestroyNode(scoped, h))
	_, err = s.cli.ContainerInspect(ctx, created.ID)
	require.Error(t, err)
}

type dockerTransferChildInput struct {
	Root  string
	Proof controlplane.OwnershipRetirementProof
	Key   ed25519.PublicKey
}

func TestDockerTransferCrashChild(t *testing.T) {
	path := os.Getenv("SYSBOX_DOCKER_TRANSFER_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var input dockerTransferChildInput
	require.NoError(t, json.Unmarshal(raw, &input))
	s, err := New()
	require.NoError(t, err)
	s.adoptionRoot = input.Root
	_, err = s.ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), input.Proof.Plan.Target), input.Proof, input.Key)
	require.NoError(t, err)
	os.Exit(77)
}
