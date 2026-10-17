package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func quiescenceFixture(t *testing.T) (*Substrate, substrate.NodeHandle, substrate.OperationIdentity, func()) {
	t.Helper()
	root := t.TempDir()
	id := substrate.OperationIdentity{ActionID: "action-1", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	vmID := "fc-quiescence"
	dir := filepath.Join(root, id.TopologyUID, id.ResourceUID, "1")
	require.NoError(t, os.MkdirAll(dir, 0700))
	cfg := filepath.Join(dir, "vm_config.json")
	require.NoError(t, os.WriteFile(cfg, []byte(`{}`), 0600))
	socket := ownedUnixSocketPath(dir, vmID, "firecracker.sock", "fc")
	require.NoError(t, ensureOwnedSocketDirectory(socket))
	hs := &HandleState{Version: manifestVersion, VMDir: dir, ConfigPath: cfg, Socket: socket}
	m := newManifest(substrate.WithOperationIdentity(context.Background(), id), root, vmID, dir, hs, "")
	require.NoError(t, refreshConfigDigest(m))
	require.NoError(t, saveManifest(m))
	s := &Substrate{rootfsDir: root}
	h := manifestHandle(m)
	cleanup := func() { _ = os.RemoveAll(root) }
	return s, h, id, cleanup
}

func TestFirecrackerProveQuiescenceUsesDurableVMOperationAnchor(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()

	proof, err := s.ProveQuiescence(context.Background(), h, id.ActionID, id)
	require.NoError(t, err)
	require.Equal(t, id.ActionID, proof.OperationID)
	require.Equal(t, id, proof.Identity)
	require.NoError(t, proof.Validate(id, id.ActionID))
}

func TestFirecrackerProveQuiescenceWaitsForHostOperationLock(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	lockRelease, err := s.lockHostOperation(context.Background(), h)
	require.NoError(t, err)
	defer lockRelease()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = s.ProveQuiescence(ctx, h, id.ActionID, id)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestFirecrackerProveQuiescenceRejectsWrongGeneration(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	id.Generation++
	_, err := s.ProveQuiescence(context.Background(), h, id.ActionID, id)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity")
}

func TestFirecrackerDeletionRetainsExactOperationQuiescence(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	id.ActionID = "destroy-action"
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	require.NoError(t, s.DestroyNode(ctx, h))
	cold := &Substrate{rootfsDir: s.rootfsDir}
	proof, err := cold.ProveQuiescence(context.Background(), h, id.ActionID, id)
	require.NoError(t, err)
	require.NoError(t, proof.Validate(id, id.ActionID))
	wrong := id
	wrong.ActionID = "another-action"
	_, err = cold.ProveQuiescence(context.Background(), h, wrong.ActionID, wrong)
	require.Error(t, err, "absence must not attest another operation")
	require.NoError(t, os.MkdirAll(h.Provider.(*HandleState).VMDir, 0700))
	_, err = cold.ProveQuiescence(context.Background(), h, id.ActionID, id)
	require.Error(t, err, "a recreated directory invalidates the deletion proof")
}

func TestFirecrackerQuiescenceDoesNotInferDaemonReturnFromUnlockedManifest(t *testing.T) {
	for _, phase := range []string{"starting", "running", "paused", "stopping", "attaching"} {
		t.Run(phase, func(t *testing.T) {
			s, h, id, cleanup := quiescenceFixture(t)
			defer cleanup()
			m, err := s.loadManifest(context.Background(), h)
			require.NoError(t, err)
			m.Phase = phase
			require.NoError(t, saveManifest(m))
			_, err = s.ProveQuiescence(context.Background(), h, id.ActionID, id)
			require.ErrorIs(t, err, substrate.ErrQuiescenceUnsupported)
		})
	}
}

func TestFirecrackerQuiescenceRejectsUnrelatedOperation(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	_, err := s.ProveQuiescence(context.Background(), h, "unrecorded-operation", id)
	require.Error(t, err)
}

func TestFirecrackerDeletionEvidenceFailurePreservesGeneration(t *testing.T) {
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	path, err := s.deletionEvidencePath(h)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(path, 0700))
	err = s.DestroyNode(substrate.WithOperationIdentity(context.Background(), id), h)
	require.Error(t, err)
	_, err = os.Stat(h.Provider.(*HandleState).ManifestPath)
	require.NoError(t, err)
}

func TestFirecrackerDeletionRejectsMissingOrCorruptEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "symlink", "identity"} {
		t.Run(mode, func(t *testing.T) {
			s, h, id, cleanup := quiescenceFixture(t)
			defer cleanup()
			require.NoError(t, s.DestroyNode(substrate.WithOperationIdentity(context.Background(), id), h))
			path, err := s.deletionEvidencePath(h)
			require.NoError(t, err)
			switch mode {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
			case "symlink":
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				target := filepath.Join(t.TempDir(), "receipt")
				require.NoError(t, os.WriteFile(target, raw, 0600))
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(target, path))
			case "identity":
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				var evidence deletionEvidence
				require.NoError(t, json.Unmarshal(raw, &evidence))
				evidence.Identity.Generation++
				raw, err = json.Marshal(evidence)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, raw, 0600))
			}
			_, err = s.ProveQuiescence(context.Background(), h, id.ActionID, id)
			require.Error(t, err)
		})
	}
}

func TestFirecrackerDeletionProcessExitBeforeRuntimeReceipt(t *testing.T) {
	if fixture := os.Getenv("SYSBOX_DELETION_TEST_FIXTURE"); fixture != "" {
		raw, err := os.ReadFile(fixture)
		require.NoError(t, err)
		var data struct {
			Root     string
			Handle   HandleState
			VMID     string
			Identity substrate.OperationIdentity
		}
		require.NoError(t, json.Unmarshal(raw, &data))
		s := &Substrate{rootfsDir: data.Root}
		h := substrate.NodeHandle{ID: data.VMID, Generation: data.Identity.Generation, Provider: &data.Handle}
		require.NoError(t, s.DestroyNode(substrate.WithOperationIdentity(context.Background(), data.Identity), h))
		os.Exit(0) // no runtime return record, defers or in-process cache survive
	}
	s, h, id, cleanup := quiescenceFixture(t)
	defer cleanup()
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	raw, err := json.Marshal(struct {
		Root     string
		Handle   HandleState
		VMID     string
		Identity substrate.OperationIdentity
	}{s.rootfsDir, *h.Provider.(*HandleState), h.ID, id})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture, raw, 0600))
	child := exec.Command(os.Args[0], "-test.run=^TestFirecrackerDeletionProcessExitBeforeRuntimeReceipt$")
	child.Env = append(os.Environ(), "SYSBOX_DELETION_TEST_FIXTURE="+fixture)
	out, err := child.CombinedOutput()
	require.NoError(t, err, string(out))
	cold := &Substrate{rootfsDir: s.rootfsDir}
	proof, err := cold.ProveQuiescence(context.Background(), h, id.ActionID, id)
	require.NoError(t, err)
	require.NoError(t, proof.Validate(id, id.ActionID))
}
