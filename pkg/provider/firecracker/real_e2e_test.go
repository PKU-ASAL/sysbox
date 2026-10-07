//go:build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func realFirecrackerPaths(t *testing.T) (string, string, string) {
	t.Helper()
	device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	require.NoError(t, err, "explicit E2E invocation requires host KVM access")
	require.NoError(t, device.Close())
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	cache = filepath.Join(cache, "sysbox")
	if configured := os.Getenv("SYSBOX_CACHE"); configured != "" {
		cache = configured
	}
	bin := os.Getenv("SYSBOX_FIRECRACKER_BIN")
	if bin == "" {
		bin, err = exec.LookPath("firecracker")
		if err != nil {
			bin = filepath.Join(cache, "tools", "firecracker")
		}
	}
	kernel := os.Getenv("SYSBOX_FIRECRACKER_KERNEL")
	if kernel == "" {
		kernel = filepath.Join(cache, "artifacts", "local", "vmlinux-5.10.245")
	}
	rootfs := os.Getenv("SYSBOX_FIRECRACKER_ROOTFS")
	if rootfs == "" {
		rootfs = filepath.Join(cache, "rootfs", "ubuntu-24.04.ext4")
	}
	for _, path := range []string{bin, kernel, rootfs} {
		require.FileExists(t, path)
	}
	return bin, kernel, rootfs
}

func realFirecracker(t *testing.T, kernel, bin, root string) *Substrate {
	t.Helper()
	s := New(kernel, root)
	s.firecrackerBin = bin
	return s
}
func cleanupRealVM(t *testing.T, s *Substrate, h substrate.NodeHandle) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, s.DestroyNode(ctx, h), "cleanup test-owned VM %s", h.ID)
	})
}

func TestFirecrackerRealGenerationLifecycleE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	identity := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, identity)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "e2e-node", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	obs, err := s.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.True(t, obs.Exists)
	require.True(t, obs.Running)
	require.Equal(t, identity.OwnerUID, obs.OwnerUID)
	require.Equal(t, identity.ResourceUID, obs.ResourceUID)
	require.Equal(t, identity.Generation, obs.Generation)
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	input := "verified stdin\n"
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "cat", Shell: substrate.ShellNone, Stdin: strings.NewReader(input)}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, input, result.Stdout)
	file := filepath.Join(t.TempDir(), "payload")
	require.NoError(t, os.WriteFile(file, []byte("verified file\n"), 0600))
	copier, ok := conn.(substrate.ConnectionFileCopier)
	require.True(t, ok)
	require.NoError(t, copier.CopyFile(ctx, file, "/tmp/sysbox-verified-file"))
	result, err = conn.Exec(ctx, substrate.ExecRequest{Program: "cat", Args: []string{"/tmp/sysbox-verified-file"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, "verified file\n", result.Stdout)
	require.NoError(t, s.CopyToNode(ctx, h, file, "/tmp/sysbox-mode-file", 0640))
	result, err = conn.Exec(ctx, substrate.ExecRequest{Program: "stat", Args: []string{"-c", "%a", "/tmp/sysbox-mode-file"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "640", strings.TrimSpace(result.Stdout))
	require.Error(t, s.CopyToNode(ctx, h, file, "/tmp", 0640), "directory must not become a successful file destination")
	require.NoError(t, s.DestroyNode(ctx, h))
}

func TestFirecrackerRealResetGenerationAndColdAdoptionE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	root := t.TempDir()
	s := realFirecracker(t, kernel, bin, root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	digest, err := digestFile(rootfs)
	require.NoError(t, err)
	baseline := substrate.ArtifactIdentity{Kind: substrate.ArtifactRootFS, Source: rootfs, Digest: digest, Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux}
	node := substrate.NodeSpec{Name: "e2e-reset", Image: substrate.ArtifactHandle{ID: rootfs, Identity: baseline}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}}
	original := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	oldCtx := substrate.WithOperationIdentity(ctx, original)
	old, err := s.CreateNode(oldCtx, node)
	require.NoError(t, err)
	cleanupRealVM(t, s, old)
	require.NoError(t, s.StartNode(oldCtx, old))
	before, err := s.ObserveNode(oldCtx, old)
	require.NoError(t, err)
	require.True(t, before.Running)
	target := original
	target.Generation = 2
	target.ActionID = "reset"
	targetCtx := substrate.WithOperationIdentity(ctx, target)
	reset, err := s.PrepareReset(targetCtx, substrate.ResetRequest{Current: old, Node: node, Baseline: baseline})
	require.NoError(t, err)
	token, err := s.MarshalResetHandle(reset)
	require.NoError(t, err)
	// Persisted reset evidence must survive loss of the original provider object.
	cold := realFirecracker(t, kernel, bin, root)
	restored, err := cold.UnmarshalResetHandle(token)
	require.NoError(t, err)
	restored.Request = reset.Request
	require.NoError(t, cold.DestroyReset(targetCtx, restored))
	require.NoError(t, cold.DestroyReset(targetCtx, restored))
	replacement, err := cold.ApplyReset(targetCtx, restored)
	require.NoError(t, err)
	cleanupRealVM(t, cold, replacement)
	require.NotEqual(t, old.ID, replacement.ID)
	require.EqualValues(t, 2, replacement.Generation)
	require.NoError(t, cold.StartNode(targetCtx, replacement))
	require.Eventually(t, func() bool {
		obs, err := cold.ObserveNode(targetCtx, replacement)
		return err == nil && obs.Running && obs.Healthy
	}, 30*time.Second, 250*time.Millisecond)
	resetObs, err := cold.ObserveReset(targetCtx, restored)
	require.NoError(t, err)
	require.True(t, resetObs.Converged, resetObs.Reason)
	require.Empty(t, resetObs.Residue)
	current, err := cold.ObserveNode(targetCtx, replacement)
	require.NoError(t, err)
	require.EqualValues(t, 2, current.Generation)
	for name, operation := range map[string]func(context.Context, substrate.NodeHandle) error{
		"start": cold.StartNode, "stop": cold.StopNode, "pause": cold.Pause, "resume": cold.Resume, "destroy": cold.DestroyNode,
	} {
		t.Run("reject-old-"+name, func(t *testing.T) {
			require.Error(t, operation(oldCtx, replacement))
			after, err := cold.ObserveNode(targetCtx, replacement)
			require.NoError(t, err)
			require.True(t, after.Running)
			require.Equal(t, current.PID, after.PID)
		})
	}
	staleHandle := replacement
	staleHandle.Generation = 1
	_, err = cold.ObserveNode(targetCtx, staleHandle)
	require.Error(t, err)
	// Deleting the already removed old handle is idempotent, not authority over
	// the replacement. Verify the replacement still lives afterward.
	require.NoError(t, cold.DestroyNode(oldCtx, old))
	after, err := cold.ObserveNode(targetCtx, replacement)
	require.NoError(t, err)
	require.True(t, after.Running)
	adopted, err := cold.DiscoverManagedNode(targetCtx, target, node.Name)
	require.NoError(t, err)
	adopted, err = cold.AdoptNode(targetCtx, adopted)
	require.NoError(t, err)
	require.Equal(t, replacement.ID, adopted.ID)
	t.Logf("reset %s (pid %d) -> %s (pid %d), generation 1 -> 2; old-generation effects rejected", old.ID, before.PID, replacement.ID, after.PID)
}

// The subprocess can only recover from persisted manifest metadata; its vmStore
// starts empty and never receives the parent's handle or process pointers.
func TestFirecrackerRealRecoveryChild(t *testing.T) {
	root := os.Getenv("SYSBOX_FC_RECOVERY_ROOT")
	if root == "" {
		t.Skip("E2E subprocess only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := realFirecracker(t, os.Getenv("SYSBOX_FC_RECOVERY_KERNEL"), os.Getenv("SYSBOX_FC_RECOVERY_BIN"), root)
	require.Empty(t, vmStore)
	id := substrate.OperationIdentity{ActionID: "observe", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.DiscoverManagedNode(ctx, id, "e2e-process")
	require.NoError(t, err)
	h, err = s.AdoptNode(ctx, h)
	require.NoError(t, err)
	require.Nil(t, lookupVM(h.Provider.(*HandleState).VMDir).snapshot().cmd)
	require.NoError(t, s.Pause(ctx, h))
	paused, err := s.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, paused.Status)
	require.NoError(t, s.Resume(ctx, h))
	obs, err := s.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.True(t, obs.Running)
	raw, err := json.Marshal(obs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "child-observation.json"), raw, 0600))
}
func TestFirecrackerRealCrossProcessRecoveryE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	root := t.TempDir()
	s := realFirecracker(t, kernel, bin, root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "e2e-process", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	before, err := s.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.True(t, before.Running)
	binary, err := os.Executable()
	require.NoError(t, err)
	child := exec.CommandContext(ctx, binary, "-test.run=^TestFirecrackerRealRecoveryChild$", "-test.v", "-test.count=1")
	child.Env = append(os.Environ(), "SYSBOX_FC_RECOVERY_ROOT="+root, "SYSBOX_FC_RECOVERY_BIN="+bin, "SYSBOX_FC_RECOVERY_KERNEL="+kernel)
	output, err := child.CombinedOutput()
	require.NoError(t, err, string(output))
	raw, err := os.ReadFile(filepath.Join(root, "child-observation.json"))
	require.NoError(t, err)
	var observed substrate.NodeObservation
	require.NoError(t, json.Unmarshal(raw, &observed))
	require.True(t, observed.Running)
	require.Equal(t, before.PID, observed.PID)
	require.Equal(t, h.ID, observed.ExternalID)
	require.Equal(t, id.OwnerUID, observed.OwnerUID)
	require.Equal(t, id.ResourceUID, observed.ResourceUID)
	require.Equal(t, id.Generation, observed.Generation)
	require.NoError(t, s.DestroyNode(ctx, h))
	t.Logf("independent process discovered/adopted/paused/resumed VM %s pid=%d generation=%d", h.ID, observed.PID, observed.Generation)
}
