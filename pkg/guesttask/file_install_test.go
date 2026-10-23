package guesttask

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func installFixture(t *testing.T) (*Engine, substrate.FileInstallRequest, SignedGrant, ed25519.PrivateKey, []byte) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	e := &Engine{Root: t.TempDir(), PublicKey: public, AllowedInstallRoots: []string{root}}
	require.NoError(t, os.Chmod(e.Root, 0700))
	body := []byte("verified installation\n")
	req := substrate.FileInstallRequest{ID: "install", Path: filepath.Join(root, "target"), Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Mode: 0640}
	digest, err := req.Digest()
	require.NoError(t, err)
	boot, err := BootID()
	require.NoError(t, err)
	g := controlplane.ExecutionGrant{TargetBootID: boot, Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command", Epoch: 1, Attempt: 1, Operation: "guest_file_install", Digest: digest, ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
	grant, err := controlplane.SignExecutionGrant(key, g, boot, time.Now())
	require.NoError(t, err)
	return e, req, grant, key, body
}
func TestFileInstallCommittedHistorySurvivesNewEpochReplacement(t *testing.T) {
	e, req, grant, key, body := installFixture(t)
	first, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.NoError(t, err)
	next := req
	next.ID = "replacement"
	next.Size = 3
	next.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
	newGrant := grant.Grant
	newGrant.ActionID = next.ID
	newGrant.CommandID = "replacement"
	newGrant.Epoch++
	newGrant.Digest, err = next.Digest()
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(key, newGrant, grant.BootID, time.Now())
	require.NoError(t, err)
	_, err = e.InstallFile(context.Background(), next, signed, strings.NewReader("new"))
	require.NoError(t, err)
	old, err := e.ObserveFileInstall(context.Background(), first.Handle)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, old.Result.Outcome)
	require.True(t, old.Result.Quiescent)
	require.NoError(t, os.Remove(req.Path))
	old, err = e.CancelFileInstall(context.Background(), first.Handle, grant)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, old.Result.Outcome)
}

func TestFileInstallCommitObserveAndCancelAreIdempotent(t *testing.T) {
	e, req, grant, _, body := installFixture(t)
	r, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, r.Result.Outcome)
	require.True(t, r.Result.Quiescent)
	raw, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, body, raw)
	info, err := os.Stat(req.Path)
	require.NoError(t, err)
	require.EqualValues(t, req.Mode, info.Mode().Perm())
	cold := *e
	cold.installHook = nil
	again, err := cold.ObserveFileInstall(context.Background(), r.Handle)
	require.NoError(t, err)
	require.Equal(t, r.Result, again.Result)
	again, err = cold.CancelFileInstall(context.Background(), r.Handle)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, again.Result.Outcome)
	again, err = cold.InstallFile(context.Background(), req, grant, bytes.NewReader([]byte("wrong")))
	require.NoError(t, err)
	require.Equal(t, r.Handle, again.Handle)
}
func TestFileInstallInterruptedBodyPreservesOriginal(t *testing.T) {
	e, req, grant, _, _ := installFixture(t)
	require.NoError(t, os.WriteFile(req.Path, []byte("original"), 0600))
	r, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader([]byte("short")))
	require.Error(t, err)
	raw, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, "original", string(raw))
	r, err = e.ObserveFileInstall(context.Background(), r.Handle)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCancelled, r.Result.Outcome)
	require.True(t, r.Result.Quiescent)
}

func TestFileInstallRejectsExtraBodyBytes(t *testing.T) {
	e, req, grant, _, body := installFixture(t)
	require.NoError(t, os.WriteFile(req.Path, []byte("original"), 0600))
	r, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader(append(body, '!')))
	require.Error(t, err)
	require.Equal(t, substrate.FileInstallCancelled, r.Result.Outcome)
	raw, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, "original", string(raw))
}
func TestFileInstallCrashWindowsRecoverWithoutRenameReplay(t *testing.T) {
	for _, phase := range []string{"prepared", "commit_intent", "renamed"} {
		t.Run(phase, func(t *testing.T) {
			e, req, grant, _, body := installFixture(t)
			require.NoError(t, os.WriteFile(req.Path, []byte("old"), 0600))
			e.installHook = func(at string) error {
				if at == phase {
					return fmt.Errorf("simulated process loss")
				}
				return nil
			}
			r, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
			require.Error(t, err)
			cold := *e
			cold.installHook = nil
			r, err = cold.ObserveFileInstall(context.Background(), r.Handle)
			require.NoError(t, err)
			want := substrate.FileInstallCancelled
			content := []byte("old")
			if phase == "renamed" {
				want = substrate.FileInstallCommitted
				content = body
			}
			require.Equal(t, want, r.Result.Outcome)
			require.True(t, r.Result.Quiescent)
			raw, err := os.ReadFile(req.Path)
			require.NoError(t, err)
			require.Equal(t, content, raw)
		})
	}
}

func TestFileInstallRejectsSymlinkAndUnsafeRootsBeforeEffects(t *testing.T) {
	for _, kind := range []string{"symlink-target", "directory-target", "symlink-parent", "writable-root", "broad-root", "control-root"} {
		t.Run(kind, func(t *testing.T) {
			e, req, grant, key, body := installFixture(t)
			outside := filepath.Join(t.TempDir(), "untouched")
			require.NoError(t, os.WriteFile(outside, []byte("original"), 0600))
			switch kind {
			case "symlink-target":
				require.NoError(t, os.Symlink(outside, req.Path))
			case "directory-target":
				require.NoError(t, os.Mkdir(req.Path, 0700))
			case "symlink-parent":
				link := filepath.Join(e.AllowedInstallRoots[0], "link")
				require.NoError(t, os.Symlink(filepath.Dir(outside), link))
				req.Path = filepath.Join(link, "untouched")
			case "writable-root":
				require.NoError(t, os.Chmod(e.AllowedInstallRoots[0], 0777))
			case "broad-root":
				e.AllowedInstallRoots = []string{"/tmp"}
			case "control-root":
				e.AllowedInstallRoots = []string{e.Root}
				req.Path = filepath.Join(e.Root, "record")
			}
			grant.Grant.Digest, _ = req.Digest()
			var err error
			grant, err = controlplane.SignExecutionGrant(key, grant.Grant, grant.BootID, time.Now())
			require.NoError(t, err)
			_, err = e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
			require.Error(t, err)
			raw, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "original", string(raw))
			_, err = os.Stat(filepath.Join(e.Root, ".installs", req.ID+".json"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestFileInstallLostAnchorRemainsUnknownAndBlocksTask(t *testing.T) {
	e, req, grant, key, body := installFixture(t)
	e.installHook = func(phase string) error {
		if phase == "commit_intent" {
			return fmt.Errorf("crash")
		}
		return nil
	}
	r, err := e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(req.Path), r.StageName, "anchor")))
	e.installHook = nil
	r, err = e.ObserveFileInstall(context.Background(), r.Handle)
	require.Error(t, err)
	require.False(t, r.Result.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, r.Result.Outcome)
	task := signedRequest(t, key, grant.BootID, "next-task")
	task.Grant.Grant.Epoch = 2
	task.Grant, err = controlplane.SignExecutionGrant(key, task.Grant.Grant, task.Grant.BootID, time.Now())
	require.NoError(t, err)
	_, err = e.Start(context.Background(), task)
	require.ErrorContains(t, err, "file install is not quiescent")
}

func TestFileInstallRejectsActiveTaskScope(t *testing.T) {
	e, req, grant, key, body := installFixture(t)
	task := signedRequest(t, key, grant.BootID, "active")
	now, err := bootNanos()
	require.NoError(t, err)
	require.NoError(t, e.save(Record{Version: RecordVersion, ID: task.ID, Digest: task.Grant.Grant.Digest, Grant: task.Grant, BootID: grant.BootID, DeadlineBootNS: now + int64(time.Minute), Status: "running"}))
	_, err = e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.ErrorContains(t, err, "guest task is not quiescent")
	_, err = os.Stat(req.Path)
	require.True(t, os.IsNotExist(err))
}

func TestFileInstallSignedCancellationTombstoneBlocksDelayedStart(t *testing.T) {
	e, req, grant, _, body := installFixture(t)
	h := substrate.FileInstallHandle{ID: req.ID, Digest: grant.Grant.Digest, BootID: grant.BootID, TopologyUID: grant.Grant.Scope.TopologyUID, ResourceUID: grant.Grant.Scope.ResourceUID, Generation: grant.Grant.Scope.Generation, Epoch: grant.Grant.Epoch}
	_, err := e.CancelFileInstall(context.Background(), h)
	require.Error(t, err, "missing unsigned request cannot mint cancellation")
	r, err := e.CancelFileInstall(context.Background(), h, grant)
	require.NoError(t, err)
	require.True(t, r.Result.Quiescent)
	require.Equal(t, "cancelled-before-start", r.Phase)
	r, err = e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCancelled, r.Result.Outcome)
	_, err = os.Stat(req.Path)
	require.True(t, os.IsNotExist(err))
	wrong := h
	wrong.BootID = "old-boot"
	_, err = e.CancelFileInstall(context.Background(), wrong, grant)
	require.Error(t, err)
}

func TestFileInstallCopyCancellationUnblocksReader(t *testing.T) {
	e, req, grant, _, _ := installFixture(t)
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	r, err := e.InstallFile(ctx, req, grant, reader)
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
	require.True(t, r.Result.Quiescent)
	require.Equal(t, substrate.FileInstallCancelled, r.Result.Outcome)
}

func TestFileInstallCancelDuringBlockedCopyPreventsRename(t *testing.T) {
	e, req, grant, _, _ := installFixture(t)
	require.NoError(t, os.WriteFile(req.Path, []byte("original"), 0600))
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := e.InstallFile(ctx, req, grant, reader); result <- err }()
	require.Eventually(t, func() bool { r, err := e.loadInstall(req.ID); return err == nil && r.Phase == "preparing" }, time.Second, 5*time.Millisecond)
	h := substrate.FileInstallHandle{ID: req.ID, Digest: grant.Grant.Digest, BootID: grant.BootID, TopologyUID: grant.Grant.Scope.TopologyUID, ResourceUID: grant.Grant.Scope.ResourceUID, Generation: grant.Grant.Scope.Generation, Epoch: grant.Grant.Epoch}
	cancelled, err := e.CancelFileInstall(ctx, h, grant)
	require.NoError(t, err)
	require.True(t, cancelled.Result.Quiescent)
	require.Equal(t, substrate.FileInstallCancelled, cancelled.Result.Outcome)
	require.Error(t, <-result)
	raw, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, "original", string(raw))
}

func TestFileInstallRejectsWritableAncestorOfTrustedRoot(t *testing.T) {
	e, req, grant, key, body := installFixture(t)
	ancestor := t.TempDir()
	require.NoError(t, os.Chmod(ancestor, 0777))
	defer os.Chmod(ancestor, 0700)
	root := filepath.Join(ancestor, "private")
	require.NoError(t, os.Mkdir(root, 0700))
	e.AllowedInstallRoots = []string{root}
	req.Path = filepath.Join(root, "target")
	grant.Grant.Digest, _ = req.Digest()
	var err error
	grant, err = controlplane.SignExecutionGrant(key, grant.Grant, grant.BootID, time.Now())
	require.NoError(t, err)
	_, err = e.InstallFile(context.Background(), req, grant, bytes.NewReader(body))
	require.Error(t, err)
}

func TestFileInstallKilledWorker(t *testing.T) {
	if os.Getenv("SYSBOX_INSTALL_TEST_CHILD") != "1" {
		return
	}
	var request substrate.FileInstallRequest
	var grant SignedGrant
	if json.Unmarshal([]byte(os.Getenv("SYSBOX_INSTALL_REQUEST")), &request) != nil || json.Unmarshal([]byte(os.Getenv("SYSBOX_INSTALL_GRANT")), &grant) != nil {
		os.Exit(2)
	}
	public, err := os.ReadFile(os.Getenv("SYSBOX_INSTALL_KEY"))
	if err != nil {
		os.Exit(2)
	}
	e := Engine{Root: os.Getenv("SYSBOX_INSTALL_STATE"), PublicKey: public, AllowedInstallRoots: []string{os.Getenv("SYSBOX_INSTALL_ALLOWED")}}
	e.installHook = func(phase string) error {
		if phase == os.Getenv("SYSBOX_INSTALL_CRASH_PHASE") {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		return nil
	}
	_, _ = e.InstallFile(context.Background(), request, grant, strings.NewReader("verified installation\n"))
	os.Exit(3)
}

func TestFileInstallRealProcessCrashRecovery(t *testing.T) {
	for _, phase := range []string{"commit_intent", "renamed"} {
		t.Run(phase, func(t *testing.T) {
			e, req, grant, _, _ := installFixture(t)
			keyPath := filepath.Join(t.TempDir(), "public")
			require.NoError(t, os.WriteFile(keyPath, e.PublicKey, 0600))
			rawReq, _ := json.Marshal(req)
			rawGrant, _ := json.Marshal(grant)
			cmd := exec.Command(os.Args[0], "-test.run=^TestFileInstallKilledWorker$")
			cmd.Env = append(os.Environ(), "SYSBOX_INSTALL_TEST_CHILD=1", "SYSBOX_INSTALL_REQUEST="+string(rawReq), "SYSBOX_INSTALL_GRANT="+string(rawGrant), "SYSBOX_INSTALL_KEY="+keyPath, "SYSBOX_INSTALL_STATE="+e.Root, "SYSBOX_INSTALL_ALLOWED="+e.AllowedInstallRoots[0], "SYSBOX_INSTALL_CRASH_PHASE="+phase)
			err := cmd.Run()
			require.Error(t, err)
			r, err := e.loadInstall(req.ID)
			require.NoError(t, err)
			r, err = e.ObserveFileInstall(context.Background(), r.Handle)
			require.NoError(t, err)
			require.True(t, r.Result.Quiescent)
			want := substrate.FileInstallCancelled
			if phase == "renamed" {
				want = substrate.FileInstallCommitted
			}
			require.Equal(t, want, r.Result.Outcome)
		})
	}
}

func TestScopedFileInstallRejectsNonTmpfsBeforeWriting(t *testing.T) {
	e, req, g, key, body := installFixture(t)
	parent, err := os.Open(filepath.Dir(req.Path))
	require.NoError(t, err)
	defer parent.Close()
	if validateFileRebootScope(parent, "guest-tmpfs") == nil {
		t.Skip("fixture filesystem is tmpfs")
	}
	req.RebootScope = "guest-tmpfs"
	digest, err := req.Digest()
	require.NoError(t, err)
	grant := g.Grant
	grant.Digest = digest
	signed, err := controlplane.SignExecutionGrant(key, grant, g.BootID, time.Now())
	require.NoError(t, err)
	_, err = e.InstallFile(context.Background(), req, signed, bytes.NewReader(body))
	require.ErrorContains(t, err, "tmpfs")
	_, err = os.Stat(req.Path)
	require.True(t, os.IsNotExist(err))
}

func TestScopedFileInstallOnRealTmpfs(t *testing.T) {
	if os.Getenv("SYSBOX_TMPFS_ACCEPTANCE") != "1" {
		t.Skip("explicit real tmpfs acceptance required")
	}
	root, err := os.MkdirTemp(os.Getenv("SYSBOX_TMPFS_ACCEPTANCE_ROOT"), "sysbox-file-reboot-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	e, req, g, key, body := installFixture(t)
	e.AllowedInstallRoots = []string{root}
	req.Path = filepath.Join(root, "target")
	req.RebootScope = "guest-tmpfs"
	digest, err := req.Digest()
	require.NoError(t, err)
	grant := g.Grant
	grant.Digest = digest
	signed, err := controlplane.SignExecutionGrant(key, grant, g.BootID, time.Now())
	require.NoError(t, err)
	record, err := e.InstallFile(context.Background(), req, signed, bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, record.Result.Outcome)
	got, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, body, got)
	require.Equal(t, "guest-tmpfs", record.Request.RebootScope)
	// Old helper decoding strips the scope, so its digest cannot authorize this request.
	legacy := req
	legacy.RebootScope = ""
	legacyDigest, err := legacy.Digest()
	require.NoError(t, err)
	require.NotEqual(t, digest, legacyDigest)
}

func TestPersistentFileStopKeepsUnknownHistoryAndRejectsReplay(t *testing.T) {
	e, req, g, key, body := installFixture(t)
	e.installHook = func(phase string) error {
		if phase == "commit_intent" {
			return fmt.Errorf("lost outcome")
		}
		return nil
	}
	original, err := e.InstallFile(context.Background(), req, g, bytes.NewReader(body))
	require.Error(t, err)
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(req.Path), original.StageName, "anchor")))
	e.installHook = nil
	foreignGrant := g.Grant
	foreignGrant.AgentID = "other-agent"
	foreign, err := controlplane.SignExecutionGrant(key, foreignGrant, g.BootID, time.Now())
	require.NoError(t, err)
	_, err = e.StopFileInstall(context.Background(), original.Handle, foreign)
	require.Error(t, err, "same handle must not authorize a different original grant")
	stopped, err := e.StopFileInstall(context.Background(), original.Handle, g)
	require.NoError(t, err)
	require.True(t, stopped.Result.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, stopped.Result.Outcome)
	require.Equal(t, g.Grant.Epoch, stopped.Result.StoppedEpoch)
	archived, err := e.loadInstall(req.ID)
	require.NoError(t, err)
	require.False(t, archived.Result.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, archived.Result.Outcome)
	_, err = e.InstallFile(context.Background(), req, g, bytes.NewReader(body))
	require.Error(t, err)
	cold := &Engine{Root: e.Root, PublicKey: e.PublicKey, AllowedInstallRoots: e.AllowedInstallRoots}
	observed, err := cold.ObserveFileInstall(context.Background(), original.Handle)
	require.NoError(t, err)
	require.Equal(t, stopped.Result, observed.Result)
	next := req
	next.ID = "successor"
	ng := g.Grant
	ng.ActionID = next.ID
	ng.Epoch++
	ng.Digest, err = next.Digest()
	require.NoError(t, err)
	signed, err := controlplane.SignExecutionGrant(key, ng, g.BootID, time.Now())
	require.NoError(t, err)
	result, err := cold.InstallFile(context.Background(), next, signed, bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, result.Result.Outcome)
	again, err := cold.StopFileInstall(context.Background(), original.Handle, g)
	require.NoError(t, err)
	require.Equal(t, stopped.Result, again.Result)
	raw, err := os.ReadFile(req.Path)
	require.NoError(t, err)
	require.Equal(t, body, raw)
}

func TestPersistentFileStopWaitsForWriterAndRetriesPartialFence(t *testing.T) {
	e, req, g, _, body := installFixture(t)
	e.installHook = func(phase string) error {
		if phase == "commit_intent" {
			return fmt.Errorf("lost outcome")
		}
		return nil
	}
	original, err := e.InstallFile(context.Background(), req, g, bytes.NewReader(body))
	require.Error(t, err)
	store := e.installStore()
	unlock, err := store.lock(context.Background(), req.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = e.StopFileInstall(ctx, original.Handle, g)
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(e.Root, ".file-stops", req.ID+".json"))
	require.True(t, os.IsNotExist(err))
	unlock()
	stopped, err := e.StopFileInstall(context.Background(), original.Handle, g)
	require.NoError(t, err)
	require.True(t, stopped.Result.Quiescent)
	// Crash boundary: scope3 durable but the per-install stop write was lost.
	require.NoError(t, os.Remove(filepath.Join(e.Root, ".file-stops", req.ID+".json")))
	_, err = e.InstallFile(context.Background(), req, g, bytes.NewReader(body))
	require.Error(t, err)
	again, err := e.StopFileInstall(context.Background(), original.Handle, g)
	require.NoError(t, err)
	require.Equal(t, stopped.Result, again.Result)
	wrong := original.Handle
	wrong.Epoch++
	_, err = e.StopFileInstall(context.Background(), wrong, g)
	require.Error(t, err)
}

func TestPersistentFileStopRejectsPriorBootTombstone(t *testing.T) {
	e, req, g, key, _ := installFixture(t)
	h := substrate.FileInstallHandle{ID: req.ID, Digest: g.Grant.Digest, BootID: "previous-guest-boot", TopologyUID: g.Grant.Scope.TopologyUID, ResourceUID: g.Grant.Scope.ResourceUID, Generation: g.Grant.Scope.Generation, Epoch: g.Grant.Epoch}
	grant := g.Grant
	grant.TargetBootID = h.BootID
	historical, err := controlplane.SignExecutionGrant(key, grant, h.BootID, g.IssuedAt)
	require.NoError(t, err)
	require.NoError(t, atomicJSON(filepath.Join(e.Root, ".file-stops", h.ID+".json"), fileStopRecord{Version: 1, Grant: historical, Handle: h, Result: substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown, Quiescent: true, StoppedEpoch: h.Epoch}}))
	_, err = e.loadFileStop(h)
	require.ErrorIs(t, err, ErrUnknown, "a prior-boot tombstone is historical evidence, not a new same-boot proof")
}
