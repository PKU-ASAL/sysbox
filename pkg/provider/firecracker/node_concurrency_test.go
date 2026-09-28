package firecracker

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

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
