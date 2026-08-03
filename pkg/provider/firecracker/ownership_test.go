package firecracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaleCleanupRejectsSocketWithoutOwnershipAnchor(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "firecracker.sock")
	require.NoError(t, os.WriteFile(socket, nil, 0600))
	err := killOwnedStaleFirecracker(dir, "vm-1", socket)
	require.ErrorContains(t, err, "ownership anchor")
	require.FileExists(t, socket)
}

func TestStaleCleanupRemovesSocketForExitedOwnedProcess(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "firecracker.sock")
	require.NoError(t, os.WriteFile(socket, nil, 0600))
	require.NoError(t, writeProcessAnchor(filepath.Join(dir, "firecracker.pid"), processAnchor{PID: 999999, StartTime: "1", Socket: socket, VMID: "vm-1"}))
	require.NoError(t, killOwnedStaleFirecracker(dir, "vm-1", socket))
	require.NoFileExists(t, socket)
}
