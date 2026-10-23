package hostsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type fileConnection struct {
	*taskConnection
	installs   int
	result     substrate.FileInstallResult
	lost       bool
	fileHandle substrate.FileInstallHandle
}

func (c *fileConnection) InstallFile(context.Context, substrate.FileInstallRequest, io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	c.installs++
	if c.lost {
		return c.fileHandle, substrate.FileInstallResult{}, errors.New("reply lost")
	}
	return c.fileHandle, c.result, nil
}
func (c *fileConnection) ObserveFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return c.result, nil
}
func (c *fileConnection) CancelFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return c.result, nil
}
func fileFixture(t *testing.T) (*FileDispatcher, TargetRef, substrate.FileInstallRequest, controlplane.SignedExecutionGrant, *fileConnection) {
	return scopedFileFixture(t, "")
}
func scopedFileFixture(t *testing.T, scope string) (*FileDispatcher, TargetRef, substrate.FileInstallRequest, controlplane.SignedExecutionGrant, *fileConnection) {
	d, old, conn, key := taskFixtureWithIssuer(t)
	data := []byte("payload")
	hash := sha256.Sum256(data)
	req := substrate.FileInstallRequest{RebootScope: scope, ID: "install", Path: "/tmp/managed-file", Mode: 0600, Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	digest, err := req.Digest()
	require.NoError(t, err)
	g := old.Grant.Grant
	g.ActionID = req.ID
	g.Digest = digest
	g.Operation = "guest_file_install"
	signed, err := controlplane.SignExecutionGrant(key, g, old.Grant.BootID, time.Now())
	require.NoError(t, err)
	ref := testRef(old)
	c := &fileConnection{taskConnection: conn, fileHandle: substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: signed.BootID, NodeID: "node", TopologyUID: g.Scope.TopologyUID, ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation, Epoch: g.Epoch}, result: substrate.FileInstallResult{Outcome: substrate.FileInstallCommitted, Quiescent: true, SHA256: req.SHA256, Size: req.Size, Mode: req.Mode}}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: c}, nil
	}
	f := &FileDispatcher{Dispatcher: d, Fetch: func(context.Context, string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	return f, ref, req, signed, c
}
func TestFileInstallLostResponseObservesWithoutReplaying(t *testing.T) {
	d, ref, req, g, c := fileFixture(t)
	c.lost = true
	h, _, err := d.Install(context.Background(), ref, req, g)
	require.Error(t, err)
	got, err := d.Observe(context.Background(), ref, h)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, got.Outcome)
	require.Equal(t, 1, c.installs)
	_, _, err = d.Install(context.Background(), ref, req, g)
	require.NoError(t, err)
	require.Equal(t, 1, c.installs)
}
func TestFileInstallRejectsContentMismatchBeforeProvider(t *testing.T) {
	d, ref, req, g, c := fileFixture(t)
	d.Fetch = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("wrong"))), nil
	}
	_, _, err := d.Install(context.Background(), ref, req, g)
	require.Error(t, err)
	require.Zero(t, c.installs)
}
func TestFileUnknownCannotCreateStopAndLostReceiptRetries(t *testing.T) {
	d, ref, req, g, c := fileFixture(t)
	c.result = substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown}
	reports := 0
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error {
		reports++
		return errors.New("ack lost")
	}
	h, _, err := d.Install(context.Background(), ref, req, g)
	require.NoError(t, err)
	require.Zero(t, reports)
	c.result = substrate.FileInstallResult{Outcome: substrate.FileInstallCancelled, Quiescent: true}
	_, err = d.Cancel(context.Background(), ref, h)
	require.Error(t, err)
	require.Equal(t, 1, reports)
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reports++; return nil }
	cold := *d
	got, err := cold.Observe(context.Background(), ref, h)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCancelled, got.Outcome)
	require.Equal(t, 2, reports)
}

type blockingInstallConnection struct {
	*fileConnection
	started, release chan struct{}
	once             sync.Once
}

func (c *blockingInstallConnection) InstallFile(context.Context, substrate.FileInstallRequest, io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	close(c.started)
	<-c.release
	return c.fileHandle, c.result, nil
}
func (c *blockingInstallConnection) CancelFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	c.once.Do(func() { close(c.release) })
	return c.result, nil
}
func TestFileCancelReachesGuestWhileInstallInFlight(t *testing.T) {
	d, ref, req, g, base := fileFixture(t)
	base.result = substrate.FileInstallResult{Outcome: substrate.FileInstallCancelled, Quiescent: true}
	c := &blockingInstallConnection{fileConnection: base, started: make(chan struct{}), release: make(chan struct{})}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: c}, nil
	}
	done := make(chan error, 1)
	go func() { _, _, err := d.Install(context.Background(), ref, req, g); done <- err }()
	t.Cleanup(func() { c.once.Do(func() { close(c.release) }); require.NoError(t, <-done) })
	<-c.started
	h := c.fileHandle
	h.NodeID = ref.Node
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := d.Cancel(ctx, ref, h)
	require.NoError(t, err)
	require.True(t, result.Quiescent)
	require.Equal(t, substrate.FileInstallCancelled, result.Outcome)
}

type rebootedFileConnection struct {
	*fileConnection
	boot string
}

func (c *rebootedFileConnection) GuestFileBootID(context.Context) (string, error) { return c.boot, nil }
func TestFileGuestRebootRetiresUnknownWithoutReinstall(t *testing.T) {
	d, ref, req, g, base := scopedFileFixture(t, "guest-tmpfs")
	base.lost = true
	conn := &rebootedFileConnection{fileConnection: base, boot: g.BootID}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: conn}, nil
	}
	h, _, err := d.Install(context.Background(), ref, req, g)
	require.Error(t, err)
	conn.boot = "new-guest"
	var receipt controlplane.SignedExecutionStopReceipt
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error { receipt = r; return nil }
	got, err := d.Cancel(context.Background(), ref, h)
	require.NoError(t, err)
	require.True(t, got.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, got.Outcome)
	require.Equal(t, "new-guest", receipt.ObservedGuestBootID)
	require.Equal(t, 1, base.installs)
	cold := *d
	again, err := cold.Observe(context.Background(), ref, h)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestUnscopedFileGuestRebootRemainsUnknown(t *testing.T) {
	d, ref, req, g, base := fileFixture(t)
	base.lost = true
	conn := &rebootedFileConnection{fileConnection: base, boot: g.BootID}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: conn}, nil
	}
	h, _, err := d.Install(context.Background(), ref, req, g)
	require.Error(t, err)
	conn.boot = "new-guest"
	reported := false
	d.ReportStop = func(context.Context, controlplane.SignedExecutionStopReceipt) error { reported = true; return nil }
	_, err = d.Cancel(context.Background(), ref, h)
	require.Error(t, err)
	require.False(t, reported)
	record, err := d.loadFile(h)
	require.NoError(t, err)
	require.Nil(t, record.Receipt)
}

type scopeStopFileConnection struct {
	*fileConnection
	stops int
}

func (c *scopeStopFileConnection) CancelFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return substrate.FileInstallResult{}, guesttask.ErrUnknown
}
func (c *scopeStopFileConnection) StopFileInstall(_ context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	c.stops++
	r := c.result
	r.Outcome = substrate.FileInstallUnknown
	r.Quiescent = true
	r.StoppedEpoch = h.Epoch
	return r, nil
}
func TestFileCancellationCanStopPersistentUnknownWithoutInventingOutcome(t *testing.T) {
	d, ref, req, g, base := fileFixture(t)
	base.result = substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown, SHA256: req.SHA256, Size: req.Size, Mode: req.Mode}
	c := &scopeStopFileConnection{fileConnection: base}
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: c}, nil
	}
	var receipt controlplane.SignedExecutionStopReceipt
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error { receipt = r; return nil }
	h, _, err := d.Install(context.Background(), ref, req, g)
	require.NoError(t, err)
	r, err := d.Cancel(context.Background(), ref, h)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallUnknown, r.Outcome)
	require.True(t, r.Quiescent)
	require.Equal(t, 1, c.stops)
	require.NoError(t, controlplane.VerifyExecutionStopReceipt(d.HostKey.Public().(ed25519.PublicKey), receipt))
	cold := *d
	r, err = cold.Observe(context.Background(), ref, h)
	require.NoError(t, err)
	require.Equal(t, 1, c.stops)
	require.Equal(t, substrate.FileInstallUnknown, r.Outcome)
}
