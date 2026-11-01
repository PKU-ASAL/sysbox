//go:build e2e

package api

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type gcProcessProof struct {
	PID           int
	Start, Socket string
}

func gcProcessStart(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return "", fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("short process stat")
	}
	return fields[19], nil
}
func captureGCProcess(t *testing.T, pid int, socket string) gcProcessProof {
	t.Helper()
	require.Greater(t, pid, 1)
	start, err := gcProcessStart(pid)
	require.NoError(t, err)
	require.FileExists(t, socket)
	return gcProcessProof{pid, start, socket}
}
func requireGCProcessGone(t *testing.T, p gcProcessProof) {
	t.Helper()
	require.Eventually(t, func() bool {
		start, err := gcProcessStart(p.PID)
		return os.IsNotExist(err) || (err == nil && start != p.Start)
	}, 5*time.Second, 10*time.Millisecond, "original process must be independently gone")
	_, err := os.Lstat(p.Socket)
	require.True(t, os.IsNotExist(err), "original socket remains: %v", err)
}
