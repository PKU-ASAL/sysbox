package windowstask

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/taskprotocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsNativeDurableTaskFaultMatrix(t *testing.T) {
	if os.Getenv("SYSBOX_WINDOWS_TASK_E2E") != "1" {
		t.Skip("requires isolated Windows acceptance VM")
	}
	e, err := LoadWindowsEngine(DefaultConfigPath)
	require.NoError(t, err)
	key, err := os.ReadFile(os.Getenv("SYSBOX_WINDOWS_TEST_ISSUER"))
	require.NoError(t, err)
	require.Len(t, key, ed25519.PrivateKeySize)
	boot, err := e.Platform.BootID()
	require.NoError(t, err)
	scope, err := e.scope()
	require.NoError(t, err)
	epoch := scope.Epoch
	sign := func(id string, args []string) taskprotocol.Request {
		epoch++
		r := taskprotocol.Request{ID: id, UserID: 65534, Args: args, OutputLimit: 1024}
		digest, err := r.Digest()
		require.NoError(t, err)
		g := controlplane.ExecutionGrant{Scope: e.Scope, AgentID: "host", SessionID: "session", CommandID: "command-" + id, ActionID: id, Attempt: 1, Epoch: epoch, Operation: "guest_task", Digest: digest, TargetBootID: boot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
		r.Grant, err = controlplane.SignExecutionGrant(key, g, boot, time.Now())
		require.NoError(t, err)
		return r
	}
	await := func(r taskprotocol.Request) taskprotocol.Record {
		var record taskprotocol.Record
		var err error
		require.Eventually(t, func() bool {
			record, err = e.Observe(context.Background(), r.ID, r.Grant.Grant.Digest)
			return err == nil && record.TerminationConfirmed
		}, 20*time.Second, 50*time.Millisecond)
		return record
	}
	suffix := time.Now().Format("150405")
	// Launch via the installed helper, not the Go test executable.
	p := e.Platform.(*WindowsPlatform)
	p.HelperBinary = filepath.Join(filepath.Dir(DefaultConfigPath), "sysbox-windows-task.exe")
	normal := sign("normal-"+suffix, []string{"cmd.exe", "/c", "echo durable-output & exit /b 7"})
	first, err := e.Start(context.Background(), normal)
	require.NoError(t, err)
	terminal := await(normal)
	require.True(t, terminal.ExitCodeKnown, terminal.Error)
	require.Equal(t, 7, terminal.ExitCode)
	cold, err := LoadWindowsEngine(DefaultConfigPath)
	require.NoError(t, err)
	cold.Platform.(*WindowsPlatform).HelperBinary = p.HelperBinary
	replay, err := cold.Start(context.Background(), normal)
	require.NoError(t, err)
	require.Equal(t, first.ID, replay.ID)
	require.Equal(t, terminal.PID, replay.PID)
	response := e.Handle(context.Background(), Call{Version: 1, Operation: "output", ID: normal.ID, Digest: normal.Grant.Grant.Digest, Stream: "stdout", Limit: 1024})
	require.Empty(t, response.Error)
	require.Contains(t, string(response.Output), "durable-output")
	// Kill the detached supervisor. Its guard must stop the whole job and retain
	// unknown exit evidence rather than relabel forced termination as success.
	running := sign("supervisor-"+suffix, []string{"cmd.exe", "/c", "ping -n 120 127.0.0.1 >NUL"})
	_, err = e.Start(context.Background(), running)
	require.NoError(t, err)
	record, err := e.Observe(context.Background(), running.ID, running.Grant.Grant.Digest)
	require.NoError(t, err)
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(record.SupervisorPID))
	require.NoError(t, err)
	require.NoError(t, windows.TerminateProcess(process, 9))
	windows.CloseHandle(process)
	stopped := await(running)
	require.False(t, stopped.ExitCodeKnown)
	require.Equal(t, "unknown", stopped.Status)
	// Cancel-before-launch writes a signed tombstone and denies the late Start.
	late := sign("late-"+suffix, []string{"cmd.exe", "/c", "echo should-not-run"})
	revoked := late.Grant.Grant
	revoked.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(key, revoked)
	require.NoError(t, err)
	_, err = e.Cancel(context.Background(), Call{ID: late.ID, Digest: revoked.Digest, Cancellation: &proof})
	require.NoError(t, err)
	_, err = cold.Start(context.Background(), late)
	require.Error(t, err)
	// A signed newer epoch fences delayed requests even in the same guest boot.
	_, err = cold.Start(context.Background(), normal)
	require.Error(t, err)
	wrongGeneration := sign("wrong-generation-"+suffix, []string{"cmd.exe", "/c", "echo forbidden"})
	wrongGrant := wrongGeneration.Grant.Grant
	wrongGrant.Scope.Generation++
	wrongGeneration.Grant, err = controlplane.SignExecutionGrant(key, wrongGrant, boot, time.Now())
	require.NoError(t, err)
	_, err = e.Start(context.Background(), wrongGeneration)
	require.Error(t, err)
	// Renewal retains the exact process; cancellation stops its whole job.
	cancelTask := sign("cancel-running-"+suffix, []string{"cmd.exe", "/c", "ping -n 120 127.0.0.1 >NUL"})
	beforeRenew, err := e.Start(context.Background(), cancelTask)
	require.NoError(t, err)
	renewed := cancelTask.Grant.Grant
	renewed.ExpiresAt = time.Now().Add(2 * time.Minute)
	renewal, err := controlplane.SignExecutionGrant(key, renewed, boot, time.Now())
	require.NoError(t, err)
	afterRenew, err := e.Renew(context.Background(), cancelTask.ID, renewed.Digest, renewal)
	require.NoError(t, err)
	require.Equal(t, beforeRenew.PID, afterRenew.PID)
	renewed.State = controlplane.GrantRevoked
	cancelProof, err := controlplane.SignExecutionCancellation(key, renewed)
	require.NoError(t, err)
	cancelled, err := e.Cancel(context.Background(), Call{ID: cancelTask.ID, Digest: renewed.Digest, Cancellation: &cancelProof})
	require.NoError(t, err)
	require.True(t, cancelled.TerminationConfirmed)
	require.False(t, cancelled.ExitCodeKnown)
	// Leave one signed task active so the Linux controller can power-cycle the
	// guest and inspect its retained record through the exact QGA UUID.
	if os.Getenv("SYSBOX_WINDOWS_REBOOT_PREPARE") == "1" {
		active := sign("reboot-"+suffix, []string{"cmd.exe", "/c", "ping -n 600 127.0.0.1 >NUL"})
		_, err = e.Start(context.Background(), active)
		require.NoError(t, err)
		raw, err := json.Marshal(active)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(DefaultConfigPath), "reboot-request.json"), raw, 0600))
		t.Log("WINDOWS_REBOOT_TASK_READY")
	}
	t.Log("native Windows normal/lost response/cold journal/supervisor crash/stale cancellation matrix passed")
}
