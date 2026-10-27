//go:build e2e

package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type crashEvidence struct {
	Handle HandleState `json:"handle"`
	ID     string      `json:"id"`
	PID    int         `json:"pid"`
}

func TestFirecrackerCrashControllerChild(t *testing.T) {
	root := os.Getenv("SYSBOX_FC_CRASH_ROOT")
	if root == "" {
		t.Skip("controller crash helper")
	}
	s := realFirecracker(t, os.Getenv("SYSBOX_FC_CRASH_KERNEL"), os.Getenv("SYSBOX_FC_CRASH_BIN"), root)
	identity := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	ctx := substrate.WithOperationIdentity(context.Background(), identity)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "crash-node", Image: substrate.ArtifactHandle{ID: os.Getenv("SYSBOX_FC_CRASH_ROOTFS")}, ProviderConfig: &Config{Kernel: os.Getenv("SYSBOX_FC_CRASH_KERNEL"), AllowDirect: true}})
	require.NoError(t, err)
	// Publish cleanup coordinates before starting any process.
	evidence := crashEvidence{Handle: *h.Provider.(*HandleState), ID: h.ID}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "controller-evidence.json"), raw, 0600))
	require.NoError(t, s.StartNode(ctx, h))
	obs, err := s.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.True(t, obs.Running)
	evidence.PID = obs.PID
	raw, err = json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "controller-evidence.json"), raw, 0600))
	fmt.Println("VM_READY")
	// The parent kills this process; no graceful teardown or deferred cleanup.
	var signal [1]byte
	_, _ = os.Stdin.Read(signal[:])
	t.Fatal("controller was expected to be killed")
}
func TestFirecrackerRealControllerCrashRecoveryE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary, err := os.Executable()
	require.NoError(t, err)
	child := exec.CommandContext(ctx, binary, "-test.run=^TestFirecrackerCrashControllerChild$", "-test.v", "-test.count=1")
	child.Env = append(os.Environ(), "SYSBOX_FC_CRASH_ROOT="+root, "SYSBOX_FC_CRASH_BIN="+bin, "SYSBOX_FC_CRASH_KERNEL="+kernel, "SYSBOX_FC_CRASH_ROOTFS="+rootfs)
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	stdin, err := child.StdinPipe()
	require.NoError(t, err)
	defer stdin.Close()
	child.Stderr = os.Stderr
	require.NoError(t, child.Start())
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
		raw, err := os.ReadFile(filepath.Join(root, "controller-evidence.json"))
		if os.IsNotExist(err) {
			return
		}
		require.NoError(t, err)
		var evidence crashEvidence
		require.NoError(t, json.Unmarshal(raw, &evidence))
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cold := realFirecracker(t, kernel, bin, root)
		require.NoError(t, cold.DestroyNode(cleanupCtx, substrate.NodeHandle{ID: evidence.ID, Generation: 1, Provider: &evidence.Handle}))
	})
	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "VM_READY" {
			ready = true
			break
		}
	}
	require.True(t, ready, "controller did not reach running VM: %v", scanner.Err())
	require.NoError(t, child.Process.Kill())
	err = child.Wait()
	waited = true
	require.Error(t, err, "controller must exit ungracefully")
	raw, err := os.ReadFile(filepath.Join(root, "controller-evidence.json"))
	require.NoError(t, err)
	var evidence crashEvidence
	require.NoError(t, json.Unmarshal(raw, &evidence))
	cold := realFirecracker(t, kernel, bin, root)
	identity := substrate.OperationIdentity{ActionID: "recover", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	recoveryCtx := substrate.WithOperationIdentity(ctx, identity)
	h, err := cold.DiscoverManagedNode(recoveryCtx, identity, "crash-node")
	require.NoError(t, err)
	h, err = cold.AdoptNode(recoveryCtx, h)
	require.NoError(t, err)
	obs, err := cold.ObserveNode(recoveryCtx, h)
	require.NoError(t, err)
	require.True(t, obs.Running)
	require.Equal(t, evidence.PID, obs.PID)
	require.Nil(t, lookupVM(h.Provider.(*HandleState).VMDir).snapshot().cmd, "parent must not inherit exec.Cmd")
	require.NoError(t, cold.Pause(recoveryCtx, h))
	require.NoError(t, cold.Resume(recoveryCtx, h))
	require.NoError(t, cold.DestroyNode(recoveryCtx, h))
	t.Logf("controller killed; surviving VM pid=%d adopted and destroyed by independent process", obs.PID)
}
