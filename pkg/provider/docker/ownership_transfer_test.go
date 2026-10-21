package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/docker/docker/api/types/container"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDockerOwnershipTransferOverridesLabelsAndFencesOldGC(t *testing.T) {
	id := strings.Repeat("a", 64)
	old := testOperation()
	old.TopologyUID = old.OwnerUID
	next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: old.Generation + 1}
	deleted := false
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted = true
			w.WriteHeader(204)
			return
		}
		if deleted {
			w.WriteHeader(404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/old-name", "Created": "2026-10-01T00:00:00Z", "Image": "sha256:image", "Config": map[string]any{"Labels": operationLabels(old)}, "State": map[string]any{"Running": false}}))
	})
	s.adoptionRoot = t.TempDir()
	evidence, err := s.InspectAdoption(context.Background(), id, substrate.NodeSpec{})
	require.NoError(t, err)
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: id, Source: old, Target: next, Fingerprint: evidence.Fingerprint}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := plan.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, s.InspectOwnershipTransfer(context.Background(), plan))
	targetCtx := substrate.WithOperationIdentity(context.Background(), next)
	h, err := s.ApplyOwnershipTransfer(targetCtx, proof, pub)
	require.NoError(t, err)
	require.Equal(t, id, h.ID)
	cold := &Substrate{cli: s.cli, adoptionRoot: s.adoptionRoot}
	require.NoError(t, cold.validateManagedHandle(targetCtx, &h))
	oldHandle := substrate.NodeHandle{ID: id, Generation: old.Generation}
	require.Error(t, cold.validateManagedHandle(substrate.WithOperationIdentity(context.Background(), old), &oldHandle))
	require.Error(t, cold.DeleteManagedExternal(context.Background(), id, old.OwnerUID, old.ResourceUID, old.Generation))
	require.False(t, deleted)
	_, err = cold.ApplyOwnershipTransfer(targetCtx, proof, pub)
	require.NoError(t, err)
	require.NoError(t, cold.DeleteManagedExternal(context.Background(), id, next.OwnerUID, next.ResourceUID, next.Generation))
	require.True(t, deleted)
}

func TestMissingTransferBindingCannotReviveOriginalLabels(t *testing.T) {
	// Missing a required binding must remain uncertainty, not label ownership.
	id := strings.Repeat("b", 64)
	s := &Substrate{adoptionRoot: t.TempDir()}
	require.NoError(t, os.WriteFile(s.transferPath(id)+".required", []byte("proof-required"), 0600))
	ins := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}, Config: &container.Config{Labels: operationLabels(testOperation())}}
	_, _, err := s.transferredLabels(ins)
	require.Error(t, err)
}

func TestTransferDiscoveryRejectsOrphanRequiredAnchor(t *testing.T) {
	s := &Substrate{adoptionRoot: t.TempDir()}
	id := strings.Repeat("c", 64)
	require.NoError(t, os.WriteFile(s.transferPath(id)+".required", []byte("required"), 0600))
	_, err := s.transferredContainerFor(testOperation())
	require.Error(t, err)
}

func TestDockerOwnershipCanTransferAgainWithoutRevivingFirstOwner(t *testing.T) {
	id := strings.Repeat("d", 64)
	old := testOperation()
	old.TopologyUID = old.OwnerUID
	s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Id": id, "Created": "created", "Image": "image", "Config": map[string]any{"Labels": operationLabels(old)}, "State": map[string]any{"Running": false}}))
	})
	s.adoptionRoot = t.TempDir()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	source := old
	for _, name := range []string{"second", "third"} {
		target := substrate.OperationIdentity{ActionID: "transfer-" + name, OwnerUID: name, TopologyUID: name, ResourceUID: "resource-" + name, Generation: source.Generation + 1}
		evidence, err := s.InspectAdoption(context.Background(), id, substrate.NodeSpec{})
		require.NoError(t, err)
		p := controlplane.OwnershipTransferPlan{Version: 1, ID: target.ActionID, SourceTopology: "source", TargetTopology: name, SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: id, Source: source, Target: target, Fingerprint: evidence.Fingerprint}
		require.NoError(t, s.InspectOwnershipTransfer(context.Background(), p))
		digest, _ := p.Digest()
		proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: p, PlanDigest: digest, SourceEpoch: uint64(source.Generation), SourceStateDigest: strings.Repeat("a", 64), RetiredAt: time.Now().UTC()})
		require.NoError(t, err)
		h, err := s.ApplyOwnershipTransfer(substrate.WithOperationIdentity(context.Background(), target), proof, pub)
		require.NoError(t, err)
		oldHandle := substrate.NodeHandle{ID: id, Generation: source.Generation}
		require.Error(t, s.validateManagedHandle(substrate.WithOperationIdentity(context.Background(), source), &oldHandle))
		require.NoError(t, s.validateManagedHandle(substrate.WithOperationIdentity(context.Background(), target), &h))
		source = target
	}
}

func TestRecreatedTransferDirectoryCannotRestoreLabelAuthority(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adoptions")
	s := &Substrate{adoptionRoot: root}
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.WriteFile(root+".transfer-required", []byte("original-catalog"), 0600))
	require.NoError(t, os.Remove(root))
	require.NoError(t, os.Mkdir(root, 0700))
	id := strings.Repeat("e", 64)
	ins := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}, Config: &container.Config{Labels: operationLabels(testOperation())}}
	_, _, err := s.transferredLabels(ins)
	require.Error(t, err)
}
