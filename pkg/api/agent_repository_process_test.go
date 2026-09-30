package api

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestAgentHeartbeatChildProcess(t *testing.T) {
	root := os.Getenv("SYSBOX_TEST_AGENT_HEARTBEAT_ROOT")
	if root == "" {
		return
	}
	s := &localAPIStore{runsDir: root}
	fmt.Println("ready")
	_, err := s.HeartbeatAgent(context.Background(), controlplane.Agent{ID: "agent", Version: "new-process"})
	require.NoError(t, err)
}
func TestAgentHeartbeatRechecksPolicyAfterProcessLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s := &localAPIStore{runsDir: t.TempDir()}
	require.NoError(t, s.SaveAgent(ctx, controlplane.Agent{ID: "agent", Status: controlplane.AgentStatusOnline}))
	// Hold a real storage transaction until the child is blocked on its lock.
	var processDone chan error
	storage := localAgentStorage{store: s}
	binary, err := os.Executable()
	require.NoError(t, err)
	_, err = storage.UpdateAgent(ctx, "agent", func(current *controlplane.Agent) (*controlplane.Agent, error) {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAgentHeartbeatChildProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SYSBOX_TEST_AGENT_HEARTBEAT_ROOT="+s.runsDir)
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		scanner := bufio.NewScanner(stdout)
		require.True(t, scanner.Scan())
		require.Equal(t, "ready", scanner.Text())
		processDone = make(chan error, 1)
		go func() {
			for scanner.Scan() {
			}
			processDone <- cmd.Wait()
		}()
		select {
		case err := <-processDone:
			t.Fatalf("heartbeat bypassed process-shared lock: %v", err)
		case <-time.After(75 * time.Millisecond):
		}
		next := *current
		next.Disabled = true
		next.Status = controlplane.AgentStatusDisabled
		next.Reason = "admin update"
		return &next, nil
	})
	require.NoError(t, err)
	select {
	case err := <-processDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("heartbeat child did not resume")
	}
	got, err := s.GetAgent(ctx, "agent")
	require.NoError(t, err)
	require.True(t, got.Disabled)
	require.Equal(t, "admin update", got.Reason)
	require.Equal(t, "new-process", got.Version)
}
