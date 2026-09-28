package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDestroyNodeDoesNotWaitOnProcessAlreadyOwnedByReaper(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	require.NoError(t, cmd.Start())
	vmID := "destroy-reaper-race"
	root := t.TempDir()
	vm := &vmProcess{cmd: cmd, pid: cmd.Process.Pid, started: true}
	vmMu.Lock()
	vmStore[filepath.Join(root, vmID)] = vm
	vmMu.Unlock()
	go reapVMProcess(vm, cmd)
	sub := New("", root)
	hs := &HandleState{}
	seedOwnedManifest(t, sub, vmID, hs, cmd)

	done := make(chan error, 1)
	go func() {
		done <- sub.DestroyNode(context.Background(), substrate.NodeHandle{ID: vmID, Provider: hs})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("DestroyNode blocked while the process reaper owned cmd.Wait")
	}
}

func TestDestroyNodeCleansOwnedHotProcessDuringExitWindow(t *testing.T) {
	root := t.TempDir()
	vmID := "destroy-exit-window"
	vmDir := filepath.Join(root, vmID)
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	socket := filepath.Join(vmDir, "firecracker.sock")
	require.NoError(t, os.WriteFile(socket, nil, 0o600))
	cmd := exec.Command("sh", "-c", "exit 0", socket)
	require.NoError(t, cmd.Start())
	startTime := processStartTime(cmd.Process.Pid)
	require.NotEmpty(t, startTime)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		cmdline, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(cmd.Process.Pid), "cmdline"))
		if err == nil && len(cmdline) == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	vmMu.Lock()
	vmStore[filepath.Join(root, vmID)] = &vmProcess{cmd: cmd, pid: cmd.Process.Pid, startTime: startTime, socket: socket, vmID: vmID, started: true}
	vmMu.Unlock()

	sub := New("", root)
	hs := &HandleState{VMDir: vmDir, Socket: socket}
	seedOwnedManifest(t, sub, vmID, hs, cmd)
	err := sub.DestroyNode(context.Background(), substrate.NodeHandle{ID: vmID, Provider: hs})
	require.NoError(t, err)
	_, err = os.Stat(vmDir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDestroyNodeUsesHotProcessIdentityInsteadOfCommandLineHeuristic(t *testing.T) {
	root := t.TempDir()
	vmID := "destroy-hot-identity"
	vmDir := filepath.Join(root, vmID)
	require.NoError(t, os.MkdirAll(vmDir, 0o755))
	socket := filepath.Join(vmDir, "firecracker.sock")
	require.NoError(t, os.WriteFile(socket, nil, 0o600))
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	startTime := processStartTime(cmd.Process.Pid)
	require.NotEmpty(t, startTime)
	t.Cleanup(func() { _ = cmd.Wait() })
	vmMu.Lock()
	vmStore[filepath.Join(root, vmID)] = &vmProcess{cmd: cmd, pid: cmd.Process.Pid, startTime: startTime, socket: socket, vmID: vmID, started: true}
	vmMu.Unlock()

	sub := New("", root)
	hs := &HandleState{VMDir: vmDir, Socket: socket}
	seedOwnedManifest(t, sub, vmID, hs, cmd)
	err := sub.DestroyNode(context.Background(), substrate.NodeHandle{ID: vmID, Provider: hs})

	require.NoError(t, err)
	_, err = os.Stat(vmDir)
	require.ErrorIs(t, err, os.ErrNotExist)
}
