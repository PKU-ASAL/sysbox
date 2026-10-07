package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The executable ignores Firecracker arguments and becomes a harmless child.
// No VM, tap, namespace, or listening socket is created by these tests.
func lifecycleFixture(t *testing.T, readiness bool) (*Substrate, substrate.NodeHandle, *vmProcess) {
	t.Helper()
	s, h, m, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	if readiness {
		hs.VsockUDS = ownedUnixSocketPath(hs.VMDir, h.ID, "vsock.sock", "vs")
		hs.VsockPort = 8901
	}
	writeFixtureManifest(t, hs.VMDir, m)
	exe, err := os.Executable()
	require.NoError(t, err)
	bin := filepath.Join(s.rootfsDir, "fixture")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\n' \"$$\" >> \"$0.starts\"\nSYSBOX_MANIFEST_PROCESS_HELPER=1 SYSBOX_MANIFEST_SOCKET=%q exec %q -test.run=^TestManifestProcessHelper$ -- \"$@\"\n", hs.Socket, exe)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	s.firecrackerBin = bin
	vm := lookupOrReserveVM(h.Provider.(*HandleState).VMDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A process can be between live and zombie /proc snapshots while its
		// test wrapper exits. Unknown must remain an error in production; retry
		// cleanup only after a fresh observation rather than weakening identity.
		require.EventuallyWithT(t, func(c *assert.CollectT) { require.NoError(c, s.DestroyNode(ctx, h)) }, 3*time.Second, time.Millisecond)
	})
	return s, h, vm
}
func TestLifecycleWaitsForStartAndHonorsCancellation(t *testing.T) {
	s, h, vm := lifecycleFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.StartNode(ctx, h) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return vm.snapshot().pid > 0 }, time.Second, time.Millisecond)
	operations := map[string]func(context.Context) error{
		"start":   func(ctx context.Context) error { return s.StartNode(ctx, h) },
		"stop":    func(ctx context.Context) error { return s.StopNode(ctx, h) },
		"destroy": func(ctx context.Context) error { return s.DestroyNode(ctx, h) },
		"pause":   func(ctx context.Context) error { return s.Pause(ctx, h) },
		"resume":  func(ctx context.Context) error { return s.Resume(ctx, h) },
		"observe": func(ctx context.Context) error { _, err := s.ObserveNode(ctx, h); return err },
		"adopt":   func(ctx context.Context) error { _, err := s.AdoptNode(ctx, h); return err },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer waitCancel()
			require.ErrorIs(t, operation(waitCtx), context.DeadlineExceeded)
		})
	}
}

func TestStartCancellationReturnsErrorAndRetainsOwnedChild(t *testing.T) {
	s, h, vm := lifecycleFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.StartNode(ctx, h) }()
	require.Eventually(t, func() bool { return vm.snapshot().pid > 0 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.FileExists(t, h.Provider.(*HandleState).ManifestPath)
	require.NoError(t, s.DestroyNode(context.Background(), h))
	require.Eventually(t, func() bool { return !processAlive(vm.snapshot().pid) }, time.Second, time.Millisecond)
}

func TestAdoptPreservesActiveChildAndReaper(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	require.NoError(t, s.StartNode(context.Background(), h))
	_, err := s.AdoptNode(context.Background(), h)
	require.NoError(t, err)
	vmMu.Lock()
	tracked := vmStore[h.Provider.(*HandleState).VMDir]
	vmMu.Unlock()
	require.Same(t, vm, tracked, "adoption must preserve the child and its reaper")
	require.NoError(t, s.StopNode(context.Background(), h))
	require.Eventually(t, func() bool { return processReaped(vm) }, time.Second, time.Millisecond)
}

func TestConcurrentStartNodeLaunchesOnlyOneChild(t *testing.T) {
	s, h, _ := lifecycleFixture(t, false)
	const callers = 24
	results := make(chan error, callers)
	for range callers {
		go func() { results <- s.StartNode(context.Background(), h) }()
	}
	for range callers {
		require.NoError(t, <-results)
	}
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(s.firecrackerBin + ".starts")
		return err == nil && len(strings.Fields(string(raw))) == 1
	}, time.Second, time.Millisecond)
}

func TestReaperRunsWhileReadinessIsPending(t *testing.T) {
	s, h, vm := lifecycleFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.StartNode(ctx, h) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return vm.snapshot().pid > 0 }, time.Second, time.Millisecond)
	state := vm.snapshot()
	require.NoError(t, state.cmd.Process.Kill())
	require.Eventually(t, func() bool { return processReaped(vm) }, time.Second, time.Millisecond)
	require.False(t, processAlive(state.pid), "the child must be reaped before readiness returns")
}

func TestLifecycleTransitionsDoNotBlockOtherVMs(t *testing.T) {
	s, h, vm := lifecycleFixture(t, true)
	other, otherHandle, _ := lifecycleFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.StartNode(ctx, h) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return vm.snapshot().pid > 0 }, time.Second, time.Millisecond)
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	require.NoError(t, other.StartNode(otherCtx, otherHandle))
}

func TestConcurrentLifecycleDestroyLeavesNoChildOrArtifacts(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	require.NoError(t, s.StartNode(context.Background(), h))
	const callers = 30
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 6 {
			case 0:
				_ = s.StartNode(context.Background(), h)
			case 1:
				_ = s.StopNode(context.Background(), h)
			case 2:
				_ = s.DestroyNode(context.Background(), h)
			case 3:
				_, _ = s.ObserveNode(context.Background(), h)
			case 4:
				_ = s.Pause(context.Background(), h)
			case 5:
				_ = s.Resume(context.Background(), h)
			}
		}()
	}
	wg.Wait()
	require.NoError(t, s.DestroyNode(context.Background(), h))
	require.NoDirExists(t, h.Provider.(*HandleState).VMDir)
	require.False(t, processAlive(vm.snapshot().pid))
	observation, err := s.ObserveNode(context.Background(), h)
	require.NoError(t, err)
	require.False(t, observation.Exists)
}

func TestRestartClearsPreviousExitState(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	require.NoError(t, s.StartNode(context.Background(), h))
	require.NoError(t, s.StopNode(context.Background(), h))
	require.Eventually(t, func() bool { return processReaped(vm) }, time.Second, time.Millisecond)
	require.NotNil(t, vm.snapshot().exitCode)
	require.NoError(t, s.StartNode(context.Background(), h))
	state := vm.snapshot()
	require.True(t, state.started)
	require.Nil(t, state.exitCode)
	require.Empty(t, state.exitError)
}

func TestDestroyWaitCancellationPreservesTrackedProcessForRetry(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	vm.mu.Lock()
	vm.cmd, vm.pid, vm.started = cmd, cmd.Process.Pid, true
	vm.startTime = processStartTime(cmd.Process.Pid)
	vm.done = make(chan struct{})
	vm.mu.Unlock()
	var once sync.Once
	seedOwnedManifest(t, s, h.ID, h.Provider.(*HandleState), cmd)
	reap := func() { once.Do(func() { go reapVMProcess(vm, cmd) }) }
	t.Cleanup(reap)
	// Delay only the fixture's reaper to expose the destroy wait deterministically.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.DestroyNode(ctx, h), context.DeadlineExceeded)
	require.Same(t, vm, lookupVM(h.Provider.(*HandleState).VMDir))
	require.DirExists(t, h.Provider.(*HandleState).VMDir)
	reap()
	require.NoError(t, s.DestroyNode(context.Background(), h))
	require.NoDirExists(t, h.Provider.(*HandleState).VMDir)
	require.False(t, processAlive(cmd.Process.Pid))
}

func TestCreateDoesNotReplaceActiveTrackedProcess(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	require.NoError(t, s.StartNode(context.Background(), h))
	_, err := s.CreateNode(context.Background(), substrate.NodeSpec{Name: h.ID})
	require.ErrorContains(t, err, "identity")
	require.Same(t, vm, lookupVM(h.Provider.(*HandleState).VMDir))
}

func TestRestoreResetInitializesColdObservationEntry(t *testing.T) {
	s, h, vm := lifecycleFixture(t, false)
	vm.mu.Lock()
	vm.cfgPath = ""
	vm.mu.Unlock()
	s.restoreResetVMProcess(h.ID, h.Provider.(*HandleState))
	require.Same(t, vm, lookupVM(h.Provider.(*HandleState).VMDir))
	require.Equal(t, h.Provider.(*HandleState).ConfigPath, vm.snapshot().cfgPath)
}

func TestVMProcessReservesOnlyOneConcurrentStart(t *testing.T) {
	vm := &vmProcess{}
	const attempts = 32
	results := make(chan bool, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- vm.reserveStart()
		}()
	}
	wg.Wait()
	close(results)

	reserved := 0
	for result := range results {
		if result {
			reserved++
		}
	}
	require.Equal(t, 1, reserved)

	vm.releaseStart()
	require.True(t, vm.reserveStart(), "a failed start must release the reservation")
}

func TestVMProcessSnapshotIsConsistent(t *testing.T) {
	vm := &vmProcess{pid: 11, startTime: "a", started: true}
	vm.mu.Lock()
	vm.pid = 42
	vm.startTime = "b"
	vm.started = false
	vm.mu.Unlock()

	snapshot := vm.snapshot()
	require.Equal(t, 42, snapshot.pid)
	require.Equal(t, "b", snapshot.startTime)
	require.False(t, snapshot.started)
}
