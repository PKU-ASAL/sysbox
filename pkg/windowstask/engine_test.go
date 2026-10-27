package windowstask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/taskprotocol"
	"github.com/stretchr/testify/require"
)

type fakePlatform struct {
	boot     string
	uptime   time.Duration
	launches int
	fail     bool
}

func (p *fakePlatform) BootID() (string, error) { return p.boot, nil }
func (p *fakePlatform) Uptime() time.Duration   { return p.uptime }
func (p *fakePlatform) Launch(context.Context, taskprotocol.Request) error {
	p.launches++
	if p.fail {
		return errors.New("launch reply lost")
	}
	return nil
}
func (p *fakePlatform) Inspect(_ context.Context, r taskprotocol.Record) (taskprotocol.Record, error) {
	return r, nil
}
func (p *fakePlatform) Stop(_ context.Context, r taskprotocol.Record) (taskprotocol.Record, error) {
	r.TerminationConfirmed = true
	r.ExitCodeKnown = false
	r.Status = "cancelled"
	return r, nil
}
func windowsFixture(t *testing.T) (*Engine, *fakePlatform, func(string, uint64) taskprotocol.Request, ed25519.PrivateKey) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	platform := &fakePlatform{boot: "boot-one", uptime: time.Minute}
	scope := controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	e := &Engine{Root: t.TempDir(), PublicKey: pub, Scope: scope, Platform: platform}
	sign := func(id string, epoch uint64) taskprotocol.Request {
		r := taskprotocol.Request{ID: id, UserID: 65534, Args: []string{"cmd.exe", "/c", "echo hi"}, OutputLimit: 1024}
		digest, err := r.Digest()
		require.NoError(t, err)
		g := controlplane.ExecutionGrant{Scope: scope, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: id, Attempt: 1, Epoch: epoch, Operation: "guest_task", Digest: digest, TargetBootID: platform.boot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
		r.Grant, err = controlplane.SignExecutionGrant(key, g, platform.boot, time.Now())
		require.NoError(t, err)
		return r
	}
	return e, platform, sign, key
}
func TestWindowsTaskLostResponseNeverRelaunches(t *testing.T) {
	e, p, sign, _ := windowsFixture(t)
	r := sign("task", 1)
	p.fail = true
	_, err := e.Start(context.Background(), r)
	require.Error(t, err)
	cold := *e
	_, err = cold.Start(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, 1, p.launches)
}
func TestWindowsTaskRebootPreservesUnknownAndRejectsOldBoot(t *testing.T) {
	e, p, sign, _ := windowsFixture(t)
	r := sign("task", 1)
	_, err := e.Start(context.Background(), r)
	require.NoError(t, err)
	p.boot = "boot-two"
	record, err := e.Observe(context.Background(), r.ID, r.Grant.Grant.Digest)
	require.NoError(t, err)
	require.Equal(t, "unknown", record.Status)
	require.False(t, record.ExitCodeKnown)
	require.True(t, record.TerminationConfirmed)
	_, err = e.Start(context.Background(), r)
	require.Error(t, err)
	_, err = e.Start(context.Background(), sign("next", 2))
	require.NoError(t, err)
	require.Equal(t, 2, p.launches)
}
func TestWindowsTaskCancellationTombstoneRejectsDelayedStart(t *testing.T) {
	e, _, sign, key := windowsFixture(t)
	r := sign("missing", 1)
	g := r.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(key, g)
	require.NoError(t, err)
	stopped, err := e.Cancel(context.Background(), Call{ID: r.ID, Digest: g.Digest, Cancellation: &proof})
	require.NoError(t, err)
	require.True(t, stopped.TerminationConfirmed)
	_, err = e.Start(context.Background(), r)
	require.Error(t, err)
	_, err = e.Start(context.Background(), sign("next", 2))
	require.NoError(t, err)
	_, err = e.Start(context.Background(), r)
	require.Error(t, err)
}
func TestWindowsTaskRejectsChangedPayloadAndScope(t *testing.T) {
	e, _, sign, _ := windowsFixture(t)
	r := sign("task", 1)
	changed := r
	changed.Args = []string{"cmd.exe", "/c", "different"}
	_, err := e.Start(context.Background(), changed)
	require.Error(t, err)
	e.Scope.Generation++
	_, err = e.Start(context.Background(), r)
	require.Error(t, err)
}
func TestWindowsTaskExpiryCannotBeRenewed(t *testing.T) {
	e, p, sign, _ := windowsFixture(t)
	r := sign("task", 1)
	_, err := e.Start(context.Background(), r)
	require.NoError(t, err)
	p.uptime += 2 * time.Minute
	_, err = e.Renew(context.Background(), r.ID, r.Grant.Grant.Digest, r.Grant)
	require.Error(t, err)
}

func TestWindowsGuardTerminalEvidenceCannotBecomeSuccessfulExit(t *testing.T) {
	r := taskprotocol.Record{ID: "task", Digest: "digest", Status: "unknown", TerminationConfirmed: true, Error: "guard stopped expired task"}
	completed := r
	completed.Status = "exited"
	completed.ExitCodeKnown = true
	completed.ExitCode = 0
	require.Equal(t, r, mergeWorkerResult(r, completed))
}

func TestWindowsPreparedCancellationPreventsWorkerClaim(t *testing.T) {
	e, p, sign, key := windowsFixture(t)
	p.fail = true
	r := sign("prepared", 1)
	_, err := e.Start(context.Background(), r)
	require.Error(t, err)
	g := r.Grant.Grant
	g.State = controlplane.GrantRevoked
	proof, err := controlplane.SignExecutionCancellation(key, g)
	require.NoError(t, err)
	stopped, err := e.Cancel(context.Background(), Call{ID: r.ID, Digest: g.Digest, Cancellation: &proof})
	require.NoError(t, err)
	require.True(t, stopped.TerminationConfirmed)
	require.True(t, stopped.CancelRequested)
	_, err = e.Start(context.Background(), r)
	require.Error(t, err)
	require.Equal(t, 1, p.launches)
}
func TestWindowsUnknownSupervisorCannotAdmitSuccessor(t *testing.T) {
	e, _, sign, _ := windowsFixture(t)
	_, err := e.Start(context.Background(), sign("first", 1))
	require.NoError(t, err)
	_, err = e.Start(context.Background(), sign("second", 2))
	require.ErrorIs(t, err, ErrUnknown)
}
func TestWindowsCorruptJournalCannotBecomeAbsent(t *testing.T) {
	e, _, sign, _ := windowsFixture(t)
	r := sign("first", 1)
	_, err := e.Start(context.Background(), r)
	require.NoError(t, err)
	path, err := e.path(r.ID, "record.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":2}`), 0600))
	_, err = e.Start(context.Background(), sign("second", 2))
	require.Error(t, err)
}

func TestWindowsLaunchAdmissionExpiresDuringPreparation(t *testing.T) {
	e, p, _, _ := windowsFixture(t)
	r := taskprotocol.Record{DeadlineBootNS: int64(p.uptime + time.Second)}
	require.NoError(t, e.checkLaunchDeadline(context.Background(), r))
	// LogonUser and guard startup consume the remaining monotonic budget.
	p.uptime += time.Second
	require.ErrorIs(t, e.checkLaunchDeadline(context.Background(), r), ErrUnauthorized)
	p.uptime -= time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, e.checkLaunchDeadline(ctx, r), context.Canceled)
	r.CancelRequested = true
	require.ErrorIs(t, e.checkLaunchDeadline(context.Background(), r), ErrUnauthorized)
}

type versionedBootPlatform struct{ *fakePlatform }

func (*versionedBootPlatform) ValidBootID(id string) bool { return len(id) > 3 && id[:3] == "v1-" }
func TestWindowsLegacyBootJournalCannotInventRebootProof(t *testing.T) {
	e, p, sign, _ := windowsFixture(t)
	r := sign("legacy", 1)
	_, err := e.Start(context.Background(), r)
	require.NoError(t, err)
	p.boot = "v1-new"
	e.Platform = &versionedBootPlatform{p}
	_, err = e.Observe(context.Background(), r.ID, r.Grant.Grant.Digest)
	require.Error(t, err)
	record, err := e.load(r.ID)
	require.NoError(t, err)
	require.False(t, record.TerminationConfirmed)
}
