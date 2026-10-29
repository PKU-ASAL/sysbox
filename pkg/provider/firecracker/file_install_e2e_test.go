//go:build e2e

package firecracker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirecrackerSignedFileInstallE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key := filepath.Join(t.TempDir(), "public")
	require.NoError(t, os.WriteFile(key, public, 0600))
	s.grantPublicKeyFile = key
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "file-owner", TopologyUID: "file-topology", ResourceUID: "file-node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "file-e2e", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	require.NoError(t, conn.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	run := func(script string) string {
		t.Helper()
		r, err := conn.Exec(ctx, substrate.ExecRequest{Program: script, Shell: substrate.ShellLinux}, nil, nil)
		require.NoError(t, err)
		require.True(t, r.ExitCodeKnown)
		require.Zero(t, r.ExitCode, r.Stderr)
		return strings.TrimSpace(r.Stdout)
	}
	run(`mkdir -p /etc/sysbox /var/tmp/sysbox-install; chmod 700 /var/tmp/sysbox-install; printf '["/var/tmp/sysbox-install"]' > /etc/sysbox/install-roots.json; chmod 600 /etc/sysbox/install-roots.json`)
	boot, err := conn.(interface {
		GuestFileBootID(context.Context) (string, error)
	}).GuestFileBootID(ctx)
	require.NoError(t, err)
	body := bytes.Repeat([]byte("file-proof"), 1024*1024)
	request := substrate.FileInstallRequest{ID: "large-install", Path: "/var/tmp/sysbox-install/large", Mode: 0640, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	sign := func(req substrate.FileInstallRequest, epoch uint64) controlplane.SignedExecutionGrant {
		t.Helper()
		digest, err := req.Digest()
		require.NoError(t, err)
		g := controlplane.ExecutionGrant{TargetBootID: boot, Scope: controlplane.ExecutionScope{TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation}, AgentID: "agent", SessionID: "session", ActionID: req.ID, CommandID: "command-" + req.ID, Attempt: 1, Epoch: epoch, Operation: "guest_file_install", Digest: digest, ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
		signed, err := controlplane.SignExecutionGrant(private, g, boot, time.Now())
		require.NoError(t, err)
		return signed
	}
	grant := sign(request, 1)
	handle, result, err := conn.(substrate.ConnectionFileInstaller).InstallFile(transport.WithTaskGrant(ctx, request.ID, grant), request, bytes.NewReader(body))
	require.NoError(t, err)
	require.True(t, result.Quiescent)
	require.Equal(t, substrate.FileInstallCommitted, result.Outcome)
	cold, err := s.Connection(h, nil)
	require.NoError(t, err)
	files := cold.(substrate.ConnectionFileInstaller)
	observed, err := files.ObserveFileInstall(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, result, observed)
	require.Equal(t, request.SHA256, strings.Fields(run("sha256sum " + request.Path))[0])
	require.Equal(t, "640", run("stat -c %a "+request.Path))
	late, err := files.CancelFileInstall(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, result, late)
	stale := handle
	stale.Generation++
	_, err = files.ObserveFileInstall(ctx, stale)
	require.Error(t, err)
	cancelledReq := request
	cancelledReq.ID = "never-arrived"
	cancelledReq.Path = "/var/tmp/sysbox-install/never"
	cancelledGrant := sign(cancelledReq, 2)
	digest, err := cancelledReq.Digest()
	require.NoError(t, err)
	pending := handle
	pending.ID = cancelledReq.ID
	pending.Digest = digest
	pending.Epoch = 2
	cancelled, err := files.CancelFileInstall(transport.WithTaskGrant(ctx, cancelledReq.ID, cancelledGrant), pending)
	require.NoError(t, err)
	require.True(t, cancelled.Quiescent)
	require.Equal(t, substrate.FileInstallCancelled, cancelled.Outcome)
	_, delayed, err := files.InstallFile(transport.WithTaskGrant(ctx, cancelledReq.ID, cancelledGrant), cancelledReq, bytes.NewReader(body))
	if err == nil {
		require.Equal(t, substrate.FileInstallCancelled, delayed.Outcome)
	}
	run("test ! -e " + cancelledReq.Path)
	require.Equal(t, request.SHA256, strings.Fields(run("sha256sum " + request.Path))[0])
	t.Log("real KVM/vsock10MiB signed install, cold Observe, actual checksum/mode, late cancel, stale generation and pre-start cancellation tombstone verified")
}
