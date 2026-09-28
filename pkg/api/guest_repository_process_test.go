package api

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGuestCASChildProcess(t *testing.T) {
	root := os.Getenv("SYSBOX_TEST_GUEST_CAS_ROOT")
	if root == "" {
		return
	}
	s := &localAPIStore{runsDir: root}
	x, err := s.GetGuestExecution(context.Background(), "shared")
	require.NoError(t, err)
	fmt.Println("ready")
	x.Status = controlplane.GuestExecutionRunning
	ok, err := s.CompareAndSwapGuestExecution(context.Background(), *x, x.Version)
	require.NoError(t, err)
	require.True(t, ok)
}
func TestGuestCASUsesProcessSharedRecordLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	s := &localAPIStore{runsDir: root}
	require.NoError(t, s.SaveGuestExecution(ctx, controlplane.GuestExecution{ID: "shared", Version: 1, Status: controlplane.GuestExecutionQueued}))
	lock := flock.New(filepath.Join(root, "_guest-executions", "shared.lock"))
	require.NoError(t, lock.Lock())
	defer lock.Close()
	binary, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestGuestCASChildProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SYSBOX_TEST_GUEST_CAS_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill() }()
	scanner := bufio.NewScanner(stdout)
	require.True(t, scanner.Scan())
	require.Equal(t, "ready", scanner.Text())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("CAS bypassed another process's record lock: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	require.NoError(t, lock.Unlock())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("CAS did not resume after unlocking")
	}
	got, err := s.GetGuestExecution(ctx, "shared")
	require.NoError(t, err)
	require.Equal(t, int64(2), got.Version)
	stat, err := os.Stat(filepath.Join(root, "_guest-executions", "shared.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
}
