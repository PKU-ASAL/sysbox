package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func signedRequest(t *testing.T, key ed25519.PrivateKey, boot, id string) Request {
	t.Helper()
	r := Request{UserID: 65534, ID: id, Args: []string{"/bin/sh", "-c", "printf output; printf error >&2; exit 7"}, OutputLimit: 1024}
	digest, err := r.Digest()
	require.NoError(t, err)
	g := SignedGrant{BootID: boot, IssuedAt: time.Now().UTC(), Grant: controlplane.ExecutionGrant{TargetBootID: boot, Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: id, CommandID: id, Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: digest, ExpiresAt: time.Now().UTC().Add(time.Minute), State: controlplane.GrantActive}}
	r.Grant, err = controlplane.SignExecutionGrant(key, g.Grant, g.BootID, g.IssuedAt)
	require.NoError(t, err)
	return r
}

func TestSignedGrantRejectsForgeryExpiredAndDifferentBoot(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := signedRequest(t, private, "boot", "task")
	require.NoError(t, verifyGrant(req.Grant, public, "boot", time.Now()))
	require.ErrorIs(t, verifyGrant(req.Grant, public, "new-boot", time.Now()), ErrUnauthorized)
	require.ErrorIs(t, verifyGrant(req.Grant, public, "boot", time.Now().Add(time.Hour)), ErrUnauthorized)
	req.Grant.Grant.Epoch++
	require.ErrorIs(t, verifyGrant(req.Grant, public, "boot", time.Now()), ErrUnauthorized)
}

func TestTaskRejectsRootWorkload(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := signedRequest(t, private, "boot", "root")
	req.UserID = 0
	require.Error(t, req.Validate())
}

func TestStartRejectsUnavailableIsolationBeforePublishingTask(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	engine := Engine{Root: t.TempDir(), CgroupRoot: t.TempDir(), PublicKey: public}
	require.NoError(t, os.Chmod(engine.Root, 0700))
	boot, err := BootID()
	require.NoError(t, err)
	_, err = engine.Start(context.Background(), signedRequest(t, private, boot, "task"))
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(engine.Root, "task", "record.json"))
	require.True(t, os.IsNotExist(err))
}

func TestTaskWorkerProcess(t *testing.T) {
	if os.Getenv("SYSBOX_GUESTTASK_WORKER") != "1" {
		return
	}
	var err error
	if os.Getenv("SYSBOX_GUESTTASK_GUARD") == "1" {
		err = RunGuard(os.Getenv("SYSBOX_GUESTTASK_ROOT"), os.Getenv("SYSBOX_GUESTTASK_CGROUP"), os.Getenv("SYSBOX_GUESTTASK_ID"))
	} else {
		err = RunWorker(os.Getenv("SYSBOX_GUESTTASK_ROOT"), os.Getenv("SYSBOX_GUESTTASK_CGROUP"), os.Getenv("SYSBOX_GUESTTASK_ID"))
	}
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestRealTaskRestartDedupOutputAndCancellation(t *testing.T) {
	t.Setenv("SYSBOX_GUESTTASK_TEST_HELPER", "1")
	cgroup := os.Getenv("SYSBOX_GUESTTASK_TEST_CGROUP")
	if cgroup == "" {
		t.Skip("requires disposable writable cgroup-v2 delegation")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	engine := Engine{Root: root, CgroupRoot: cgroup, PublicKey: public, WorkerBinary: os.Args[0], WorkerArgs: []string{"-test.run=^TestTaskWorkerProcess$"}}
	boot, err := BootID()
	require.NoError(t, err)
	request := signedRequest(t, private, boot, "result")
	first, err := engine.Start(context.Background(), request)
	require.NoError(t, err)
	second, err := engine.Start(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, first.Digest, second.Digest)
	reopened := Engine{Root: root, CgroupRoot: cgroup, PublicKey: public}
	var result Record
	require.Eventually(t, func() bool {
		result, err = reopened.Observe(context.Background(), request.ID, first.Digest)
		return err == nil && result.TerminationConfirmed
	}, 5*time.Second, 20*time.Millisecond)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, 7, result.ExitCode)
	out, next, err := reopened.ReadOutput(context.Background(), request.ID, first.Digest, "stdout", 0, 100)
	require.NoError(t, err)
	require.Equal(t, "output", string(out))
	require.EqualValues(t, 6, next)
	changed := request
	changed.Args = []string{"touch", "/must-not-exist"}
	_, err = engine.Start(context.Background(), changed)
	require.Error(t, err)
	running := signedRequest(t, private, boot, "cancel")
	running.Args = []string{"/bin/sh", "-c", "sleep 60 & wait"}
	running.Grant.Grant.Digest, _ = running.Digest()
	running.Grant, err = controlplane.SignExecutionGrant(private, running.Grant.Grant, running.Grant.BootID, running.Grant.IssuedAt)
	require.NoError(t, err)
	task, err := engine.Start(context.Background(), running)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		task, err = reopened.Observe(context.Background(), running.ID, task.Digest)
		return err == nil && task.PID > 0
	}, 5*time.Second, 20*time.Millisecond)
	wrong := Call{ID: task.ID, Digest: task.Digest, BootID: "other-boot", Epoch: task.Grant.Grant.Epoch, Scope: &task.Grant.Grant.Scope}
	require.ErrorIs(t, reopened.CancelWithProof(context.Background(), wrong), ErrConflict)
	unchanged, err := reopened.Observe(context.Background(), task.ID, task.Digest)
	require.NoError(t, err)
	require.False(t, unchanged.CancelRequested)
	stopGrant := task.Grant.Grant
	stopGrant.State = controlplane.GrantRevoked
	stopProof, err := controlplane.SignExecutionCancellation(private, stopGrant)
	require.NoError(t, err)
	require.NoError(t, reopened.CancelWithProof(context.Background(), Call{ID: task.ID, Digest: task.Digest, BootID: task.BootID, Epoch: stopGrant.Epoch, Scope: &stopGrant.Scope, Cancellation: &stopProof}))
	require.Eventually(t, func() bool {
		task, err = reopened.Observe(context.Background(), task.ID, task.Digest)
		return err == nil && task.TerminationConfirmed
	}, 5*time.Second, 20*time.Millisecond)
	require.True(t, task.CancelRequested)
	t.Run("unprivileged_no_new_privileges", func(t *testing.T) {
		request := signedRequest(t, private, boot, "isolation")
		request.Args = []string{"/bin/sh", "-c", fmt.Sprintf("test $(id -u) = 65534 && ! test -r %s && awk '/^NoNewPrivs:/ {if ($2 != 1) exit 1; found=1} END {if (!found) exit 1}' /proc/self/status && awk '/^CapEff:/ {if ($2 != \"0000000000000000\") exit 1}' /proc/self/status", filepath.Join(root, "isolation", "record.json"))}
		request.Grant.Grant.Digest, _ = request.Digest()
		request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, request.Grant.IssuedAt)
		require.NoError(t, err)
		record, err := engine.Start(context.Background(), request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			record, err = engine.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.TerminationConfirmed
		}, 5*time.Second, 20*time.Millisecond)
		require.True(t, record.ExitCodeKnown)
		require.Zero(t, record.ExitCode, record.Error)
	})

	t.Run("supervisor_crash", func(t *testing.T) {
		request := signedRequest(t, private, boot, "supervisor-crash")
		request.Args = []string{"/bin/sh", "-c", "sleep 60 & wait"}
		request.Grant.Grant.Digest, _ = request.Digest()
		request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, request.Grant.IssuedAt)
		require.NoError(t, err)
		record, err := engine.Start(context.Background(), request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			record, err = engine.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.PID > 0
		}, 5*time.Second, 20*time.Millisecond)
		require.NoError(t, syscall.Kill(record.SupervisorPID, syscall.SIGKILL))
		require.Eventually(t, func() bool {
			record, err = reopened.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.TerminationConfirmed
		}, 5*time.Second, 20*time.Millisecond)
		require.False(t, record.ExitCodeKnown)
		require.Equal(t, "unknown", record.Status)
	})
	t.Run("expiry_and_bounded_output", func(t *testing.T) {
		request := signedRequest(t, private, boot, "expires")
		request.Args = []string{"/bin/sh", "-c", "while :; do printf 01234567890123456789; done"}
		request.OutputLimit = 17
		request.Grant.Grant.ExpiresAt = time.Now().Add(1250 * time.Millisecond)
		request.Grant.Grant.Digest, _ = request.Digest()
		request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, request.Grant.IssuedAt)
		require.NoError(t, err)
		record, err := engine.Start(context.Background(), request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			record, err = reopened.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.TerminationConfirmed
		}, 5*time.Second, 20*time.Millisecond)
		require.True(t, record.CancelRequested)
		require.True(t, record.StdoutTruncated)
		output, _, err := reopened.ReadOutput(context.Background(), record.ID, record.Digest, "stdout", 0, 100)
		require.NoError(t, err)
		require.Len(t, output, 17)
	})
	t.Run("kernel_510_freeze_pidfd_retirement", func(t *testing.T) {
		request := signedRequest(t, private, boot, "freeze-kill")
		request.Args = []string{"/bin/sh", "-c", "sleep 60 & wait"}
		request.Grant.Grant.Digest, _ = request.Digest()
		request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, request.Grant.IssuedAt)
		require.NoError(t, err)
		record, err := engine.Start(context.Background(), request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			record, err = engine.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.PID > 0
		}, 5*time.Second, 20*time.Millisecond)
		group, err := engine.openGroup(record)
		require.NoError(t, err)
		defer group.Close()
		require.NoError(t, freezeAndKill(group))
		require.Eventually(t, func() bool { empty, err := groupEmpty(group); return err == nil && empty }, 5*time.Second, 20*time.Millisecond)
	})
	t.Run("renewal_and_epoch_fencing", func(t *testing.T) {
		request := signedRequest(t, private, boot, "renew")
		request.Args = []string{"/bin/sh", "-c", "sleep 60 & wait"}
		request.Grant.Grant.ExpiresAt = time.Now().Add(1300 * time.Millisecond)
		request.Grant.Grant.Digest, _ = request.Digest()
		request.Grant, err = controlplane.SignExecutionGrant(private, request.Grant.Grant, boot, request.Grant.IssuedAt)
		require.NoError(t, err)
		record, err := engine.Start(context.Background(), request)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			record, err = engine.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.PID > 0
		}, 5*time.Second, 20*time.Millisecond)
		grant := request.Grant.Grant
		grant.ExpiresAt = time.Now().Add(2 * time.Second)
		signed, err := controlplane.SignExecutionGrant(private, grant, boot, time.Now())
		require.NoError(t, err)
		_, err = engine.Renew(context.Background(), record.ID, record.Digest, signed)
		require.NoError(t, err)
		time.Sleep(350 * time.Millisecond)
		record, err = engine.Observe(context.Background(), record.ID, record.Digest)
		require.NoError(t, err)
		require.False(t, record.TerminationConfirmed)
		newer := signedRequest(t, private, boot, "new-epoch")
		newer.Grant.Grant.Epoch = 2
		newer.Grant, err = controlplane.SignExecutionGrant(private, newer.Grant.Grant, boot, newer.Grant.IssuedAt)
		require.NoError(t, err)
		_, err = engine.Start(context.Background(), newer)
		require.Error(t, err, "new epoch cannot overlap live old task")
		require.NoError(t, engine.Cancel(context.Background(), record.ID, record.Digest))
		require.Eventually(t, func() bool {
			record, err = engine.Observe(context.Background(), record.ID, record.Digest)
			return err == nil && record.TerminationConfirmed
		}, 5*time.Second, 20*time.Millisecond)
		_, err = engine.Renew(context.Background(), record.ID, record.Digest, signed)
		require.Error(t, err, "cancelled grant must not resurrect")
		_, err = engine.Start(context.Background(), newer)
		require.NoError(t, err)
		_, err = engine.Start(context.Background(), request)
		require.Error(t, err, "old epoch remains rejected after takeover")
	})
}
