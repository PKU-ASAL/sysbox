package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedCancellationSealsMissingTaskAgainstLateStart(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	req := signedRequest(t, key, boot, "never-started")
	req.Grant.Grant.ActionID = req.ID
	req.Grant, err = controlplane.SignExecutionGrant(key, req.Grant.Grant, boot, time.Now())
	require.NoError(t, err)
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	stop, err := controlplane.SignExecutionCancellation(key, g)
	require.NoError(t, err)
	engine := &Engine{Root: t.TempDir(), CgroupRoot: "/sys/fs/cgroup", PublicKey: pub}
	require.NoError(t, os.Chmod(engine.Root, 0700))
	call := Call{ID: req.ID, Digest: g.Digest, BootID: boot, Epoch: g.Epoch, Scope: &g.Scope, Cancellation: &stop}
	require.NoError(t, engine.CancelWithProof(context.Background(), call))
	cold := *engine
	record, err := cold.Observe(context.Background(), req.ID, g.Digest)
	require.NoError(t, err)
	require.True(t, record.TerminationConfirmed)
	require.True(t, record.CancelRequested)
	require.False(t, record.ExitCodeKnown)
	late, err := cold.Start(context.Background(), req)
	require.NoError(t, err)
	require.True(t, late.TerminationConfirmed)
	require.Zero(t, late.PID)
	require.NoError(t, cold.CancelWithProof(context.Background(), call))
}

func missingCancellationFixture(t *testing.T) (*Engine, Request, Call, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	req := signedRequest(t, key, boot, "task")
	g := req.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(key, g)
	require.NoError(t, err)
	e := &Engine{Root: t.TempDir(), CgroupRoot: "/sys/fs/cgroup", PublicKey: pub}
	require.NoError(t, os.Chmod(e.Root, 0700))
	return e, req, Call{ID: req.ID, Digest: g.Digest, BootID: boot, Epoch: g.Epoch, Scope: &g.Scope, Cancellation: &proof}, key
}

func TestMissingCancellationRejectsUnsignedAndMismatchedProof(t *testing.T) {
	for _, kind := range []string{"unsigned", "signature", "boot", "guest-reboot", "scope", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			e, req, call, key := missingCancellationFixture(t)
			switch kind {
			case "unsigned":
				call.Cancellation = nil
			case "signature":
				call.Cancellation.Signature[0] ^= 1
			case "boot":
				call.BootID = "other"
			case "guest-reboot":
				g := call.Cancellation.Grant
				g.TargetBootID = "previous-boot"
				proof, err := controlplane.SignExecutionCancellation(key, g)
				require.NoError(t, err)
				call.Cancellation = &proof
				call.BootID = g.TargetBootID
			case "scope":
				scope := *call.Scope
				scope.ResourceUID = "other"
				call.Scope = &scope
			case "epoch":
				call.Epoch++
			}
			require.Error(t, e.CancelWithProof(context.Background(), call))
			_, err := os.Stat(filepath.Join(e.taskPath(req.ID), "record.json"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestMissingCancellationRecoversTaskDirectoryWithoutRecord(t *testing.T) {
	e, req, call, _ := missingCancellationFixture(t)
	require.NoError(t, os.Mkdir(e.taskPath(req.ID), 0700))
	require.NoError(t, e.CancelWithProof(context.Background(), call))
	got, err := e.Observe(context.Background(), req.ID, req.Grant.Grant.Digest)
	require.NoError(t, err)
	require.True(t, got.TerminationConfirmed)
}

func TestMissingCancellationRecoversScopeCommitBeforeRecordFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission fault requires unprivileged process")
	}
	e, req, call, _ := missingCancellationFixture(t)
	dir := e.taskPath(req.ID)
	require.NoError(t, os.Mkdir(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	require.Error(t, e.CancelWithProof(context.Background(), call))
	_, scope, unlock, err := e.lockScope(context.Background(), call.Cancellation.Grant)
	require.NoError(t, err)
	unlock()
	require.Equal(t, call.Epoch, scope.Epoch)
	_, err = os.Stat(filepath.Join(dir, "record.json"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, os.Chmod(dir, 0700))
	cold := *e
	require.NoError(t, cold.CancelWithProof(context.Background(), call))
	stopped, err := cold.Observe(context.Background(), req.ID, req.Grant.Grant.Digest)
	require.NoError(t, err)
	require.True(t, stopped.TerminationConfirmed)
}

func TestMissingCancellationRejectsDanglingRecordReference(t *testing.T) {
	e, req, call, _ := missingCancellationFixture(t)
	dir := e.taskPath(req.ID)
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "record.json")))
	require.ErrorIs(t, e.CancelWithProof(context.Background(), call), ErrUnknown)
}

func TestMissingCancellationDoesNotIgnoreOtherMissingRecords(t *testing.T) {
	e, req, call, _ := missingCancellationFixture(t)
	require.NoError(t, os.Mkdir(e.taskPath("other-task"), 0700))
	require.Error(t, e.CancelWithProof(context.Background(), call))
	_, err := os.Stat(filepath.Join(e.taskPath(req.ID), "record.json"))
	require.True(t, os.IsNotExist(err))
}

func TestRealMissingTaskCancellationRejectsOrphanCgroup(t *testing.T) {
	root := os.Getenv("SYSBOX_GUESTTASK_TEST_CGROUP")
	if root == "" {
		t.Skip("requires disposable writable cgroup-v2 delegation")
	}
	e, req, call, _ := missingCancellationFixture(t)
	e.CgroupRoot = root
	group := e.group(req.ID)
	require.NoError(t, os.Mkdir(group, 0700))
	t.Cleanup(func() { require.NoError(t, os.Remove(group)) })
	require.ErrorIs(t, e.CancelWithProof(context.Background(), call), ErrUnknown)
	_, err := os.Stat(filepath.Join(e.taskPath(req.ID), "record.json"))
	require.True(t, os.IsNotExist(err), "an orphan cgroup must not become absence evidence")
}

func TestRealMissingTaskCancellationPreventsWorkerLaunch(t *testing.T) {
	root := os.Getenv("SYSBOX_GUESTTASK_TEST_CGROUP")
	if root == "" {
		t.Skip("requires disposable writable cgroup-v2 delegation")
	}
	t.Setenv("SYSBOX_GUESTTASK_TEST_HELPER", "1")
	e, req, call, _ := missingCancellationFixture(t)
	e.CgroupRoot = root
	e.WorkerBinary = os.Args[0]
	e.WorkerArgs = []string{"-test.run=^TestTaskWorkerProcess$"}
	require.NoError(t, e.CancelWithProof(context.Background(), call))
	stopped, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	require.True(t, stopped.TerminationConfirmed)
	require.True(t, stopped.CancelRequested)
	require.Zero(t, stopped.PID)
	_, err = os.Stat(e.group(req.ID))
	require.True(t, os.IsNotExist(err))
}

func TestMissingCancellationReplaysWithoutRegressingSuccessor(t *testing.T) {
	e, req, call, key := missingCancellationFixture(t)
	ctx := context.Background()
	require.NoError(t, e.CancelWithProof(ctx, call))
	newer := call.Cancellation.Grant
	newer.Epoch++
	newer.ActionID = "successor"
	newer.CommandID = "successor"
	newer.Digest = "successor-digest"
	proof, err := controlplane.SignExecutionCancellation(key, newer)
	require.NoError(t, err)
	require.NoError(t, e.CancelWithProof(ctx, Call{ID: newer.ActionID, Digest: newer.Digest, BootID: newer.TargetBootID, Epoch: newer.Epoch, Scope: &newer.Scope, Cancellation: &proof}))
	require.NoError(t, e.CancelWithProof(ctx, call))
	_, scope, unlock, err := e.lockScope(ctx, newer)
	require.NoError(t, err)
	unlock()
	require.Equal(t, newer.Epoch, scope.Epoch)
	old := call.Cancellation.Grant
	old.ActionID = "old-missing"
	old.CommandID = "old-missing"
	old.Digest = "old-digest"
	oldProof, err := controlplane.SignExecutionCancellation(key, old)
	require.NoError(t, err)
	require.ErrorIs(t, e.CancelWithProof(ctx, Call{ID: old.ActionID, Digest: old.Digest, BootID: old.TargetBootID, Epoch: old.Epoch, Scope: &old.Scope, Cancellation: &oldProof}), ErrUnauthorized)
	stopped, err := e.Observe(ctx, req.ID, req.Grant.Grant.Digest)
	require.NoError(t, err)
	require.True(t, stopped.TerminationConfirmed)
}

func TestSignedCancellationDoesNotInventAbsenceForPreparedTask(t *testing.T) {
	e, req, call, _ := missingCancellationFixture(t)
	now, err := bootNanos()
	require.NoError(t, err)
	record := Record{Version: RecordVersion, ID: req.ID, Digest: req.Grant.Grant.Digest, Grant: req.Grant, BootID: req.Grant.BootID, DeadlineBootNS: now + int64(time.Minute), Status: "prepared"}
	require.NoError(t, e.save(record))
	require.NoError(t, e.CancelWithProof(context.Background(), call))
	got, err := e.Observe(context.Background(), req.ID, record.Digest)
	require.NoError(t, err)
	require.True(t, got.CancelRequested)
	require.False(t, got.TerminationConfirmed)
	require.Nil(t, got.Cancellation)
}

func TestCancellationTombstoneRejectsMissingProofAndOldFormat(t *testing.T) {
	for _, kind := range []string{"missing-proof", "tampered-proof", "old-format"} {
		t.Run(kind, func(t *testing.T) {
			e, req, call, _ := missingCancellationFixture(t)
			require.NoError(t, e.CancelWithProof(context.Background(), call))
			record, err := e.load(req.ID)
			require.NoError(t, err)
			switch kind {
			case "missing-proof":
				record.Cancellation = nil
			case "tampered-proof":
				record.Cancellation.Signature[0] ^= 1
			case "old-format":
				record.Version = 1
			}
			require.NoError(t, e.save(record))
			_, err = e.Observe(context.Background(), req.ID, req.Grant.Grant.Digest)
			require.Error(t, err)
		})
	}
}

func TestTaskStartRejectsSignedActionMismatch(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	req := signedRequest(t, key, boot, "task")
	req.Grant.Grant.ActionID = "other"
	req.Grant, err = controlplane.SignExecutionGrant(key, req.Grant.Grant, boot, time.Now())
	require.NoError(t, err)
	engine := &Engine{Root: t.TempDir(), CgroupRoot: "/sys/fs/cgroup", PublicKey: pub}
	require.NoError(t, os.Chmod(engine.Root, 0700))
	_, err = engine.Start(context.Background(), req)
	require.ErrorIs(t, err, ErrUnauthorized)
}
