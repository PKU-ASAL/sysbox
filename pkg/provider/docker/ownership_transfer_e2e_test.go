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
	"github.com/docker/docker/errdefs"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDockerOwnershipTransferColdLifecycleE2E(t *testing.T) {
	testDockerTransferHostBoundary(t, "normal")
}

// This tests the real Docker provider and host coordinator. Report delivery is
// a callback boundary; it does not cover API state persistence or API crashes.
func TestDockerOwnershipTransferHostSIGKILLMatrixE2E(t *testing.T) {
	for _, phase := range []string{"before-effect", "after-ownership", "after-observe", "before-report", "after-report"} {
		t.Run(phase, func(t *testing.T) { testDockerTransferHostBoundary(t, phase) })
	}
}

func testDockerTransferHostBoundary(t *testing.T, phase string) {
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
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := s.cli.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
		if err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("cleanup disposable container %s: %v", created.ID, err)
		}
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
	input := dockerTransferChildInput{Root: s.adoptionRoot, Proof: proof, Key: pub, Phase: phase, Ledger: t.TempDir(), Ready: filepath.Join(t.TempDir(), "ready")}
	_, input.Host, err = ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	input.Request = controlplane.AdoptionRequest{Transfer: &proof, ID: p.ID, Topology: "target", TopologyUID: next.TopologyUID, Address: p.TargetAddress, Provider: "docker", AgentID: "host", ExternalID: created.ID, ResourceUID: next.ResourceUID, Generation: next.Generation, Evidence: evidence}
	requestDigest, err := input.Request.Digest()
	require.NoError(t, err)
	now := time.Now().UTC()
	g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: next.TopologyUID, ResourceUID: next.ResourceUID, Generation: next.Generation}, AgentID: "host", SessionID: "session", ActionID: p.ID, CommandID: "command", Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: requestDigest, TargetBootID: "boot", State: controlplane.GrantActive, ExpiresAt: now.Add(time.Minute)}
	input.Grant, err = controlplane.SignExecutionGrant(key, g, "boot", now)
	require.NoError(t, err)
	childRaw, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inputPath, childRaw, 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestDockerTransferCrashChild$")
	child.Env = append(os.Environ(), "SYSBOX_DOCKER_TRANSFER_CHILD="+inputPath)
	if phase == "normal" {
		output, childErr := child.CombinedOutput()
		var exited *exec.ExitError
		require.ErrorAs(t, childErr, &exited, string(output))
		require.Equal(t, 77, exited.ExitCode(), string(output))
	} else {
		log, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
		require.NoError(t, err)
		defer log.Close()
		child.Stdout, child.Stderr = log, log
		require.NoError(t, child.Start())
		defer child.Process.Kill()
		require.Eventually(t, func() bool { _, err := os.Stat(input.Ready); return err == nil }, 15*time.Second, 20*time.Millisecond, "child log: %s", log.Name())
		require.NoError(t, child.Process.Kill())
		var exited *exec.ExitError
		require.ErrorAs(t, child.Wait(), &exited)
		require.Equal(t, -1, exited.ExitCode())
		before, err := s.InspectAdoption(ctx, created.ID, substrate.NodeSpec{})
		require.NoError(t, err)
		expected := next
		if phase == "before-effect" {
			expected = old
		}
		ins, err := s.cli.ContainerInspect(ctx, created.ID)
		require.NoError(t, err)
		labels, err := s.managedOwnershipLabels(ctx, ins)
		require.NoError(t, err)
		require.NoError(t, verifyLabels(labels, operationLabels(expected)))
		reports := 0
		coordinator := hostsupervisor.AdoptionCoordinator{Root: input.Ledger, IssuerKey: pub, HostKey: input.Host, AgentID: "host", HostBootID: "boot", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(_ context.Context, id string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
			require.Equal(t, p.ID, id)
			require.NoError(t, controlplane.VerifyAdoptionStopReceipt(input.Host.Public().(ed25519.PublicKey), receipt, evidence))
			require.Equal(t, input.Grant.Grant, receipt.Grant)
			require.NotEmpty(t, evidence)
			reports++
			return nil
		}}
		require.NoError(t, coordinator.Recover(ctx))
		_, err = coordinator.Observe(ctx, input.Request, input.Grant)
		if phase == "before-report" || phase == "after-report" {
			require.NoError(t, err)
			if phase == "before-report" {
				require.Positive(t, reports)
			} else {
				require.Zero(t, reports, "durably acknowledged report is not resent")
				raw, err := os.ReadFile(filepath.Join(input.Root, "reported.json"))
				require.NoError(t, err)
				var reported dockerCrashReport
				require.NoError(t, json.Unmarshal(raw, &reported))
				require.NoError(t, controlplane.VerifyAdoptionStopReceipt(input.Host.Public().(ed25519.PublicKey), reported.Receipt, reported.Evidence))
				require.Equal(t, input.Grant.Grant, reported.Receipt.Grant)
			}
		} else {
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			require.Zero(t, reports)
		}
		_, err = coordinator.Apply(ctx, input.Request, input.Grant, func(context.Context) (json.RawMessage, error) {
			t.Error("provider replay after SIGKILL")
			return nil, nil
		})
		require.Error(t, err)
		after, err := s.InspectAdoption(ctx, created.ID, substrate.NodeSpec{})
		require.NoError(t, err)
		require.Equal(t, before, after)
		afterIns, err := s.cli.ContainerInspect(ctx, created.ID)
		require.NoError(t, err)
		afterLabels, err := s.managedOwnershipLabels(ctx, afterIns)
		require.NoError(t, err)
		require.Equal(t, labels, afterLabels)
		if phase == "before-effect" || phase == "after-ownership" || phase == "after-observe" {
			return
		}
	}
	recovered, recoveryErr := s.InspectAdoptionRecovery(scoped, controlplane.AdoptionRequest{ExternalID: created.ID})
	require.NoError(t, recoveryErr, "read-only ownership recovery after lost provider return")
	require.Equal(t, created.ID, recovered.ID)
	require.Equal(t, p.Target.Generation, recovered.Generation)
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
	Root                 string
	Phase, Ledger, Ready string
	Request              controlplane.AdoptionRequest
	Grant                controlplane.SignedExecutionGrant
	Host                 ed25519.PrivateKey
	Proof                controlplane.OwnershipRetirementProof
	Key                  ed25519.PublicKey
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
	if input.Phase != "normal" {
		boundary := func(phase string) {
			if input.Phase == phase {
				require.NoError(t, os.WriteFile(input.Ready, []byte(phase), 0600))
				select {}
			}
		}
		coordinator := hostsupervisor.AdoptionCoordinator{Root: input.Ledger, IssuerKey: input.Key, HostKey: input.Host, AgentID: "host", HostBootID: "boot", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, Report: func(_ context.Context, _ string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
			boundary("before-report")
			raw, err := json.Marshal(dockerCrashReport{Receipt: receipt, Evidence: evidence})
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(input.Root, "reported.json"), raw, 0600)
		}}
		_, err = coordinator.Apply(context.Background(), input.Request, input.Grant, func(ctx context.Context) (json.RawMessage, error) {
			boundary("before-effect")
			scoped := substrate.WithOperationIdentity(ctx, input.Proof.Plan.Target)
			h, err := s.ApplyOwnershipTransfer(scoped, input.Proof, input.Key)
			if err != nil {
				return nil, err
			}
			boundary("after-ownership")
			if _, err = s.ObserveNode(scoped, h); err != nil {
				return nil, err
			}
			boundary("after-observe")
			return s.MarshalProviderState(h)
		})
		require.NoError(t, err)
		boundary("after-report")
		t.Fatal("child did not reach requested boundary")
	}
	_, err = s.ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), input.Proof.Plan.Target), input.Proof, input.Key)
	require.NoError(t, err)
	os.Exit(77)
}

type dockerCrashReport struct {
	Receipt  controlplane.SignedExecutionStopReceipt
	Evidence json.RawMessage
}
