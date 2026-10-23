//go:build e2e

package firecracker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/provider/firecracker/initbin"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

// The test-only guest probe creates a real uncertain install using the original
// signed request and production Engine. Only its final checkpoint is faulted.
// The production vsock helper then reads that exact journal and performs stop.
func TestFirecrackerUnknownPersistentFileStopE2E(t *testing.T) {
	probe := os.Getenv("SYSBOX_FILE_FENCE_PROBE")
	require.NotEmpty(t, probe, "current static guesttask acceptance probe required")
	embedded, err := initbin.Bytes()
	require.NoError(t, err)
	expectedInit := os.Getenv("SYSBOX_EXPECTED_INIT_SHA256")
	require.NotEmpty(t, expectedInit, "wrapper-built current helper hash required")
	require.Equal(t, expectedInit, fmt.Sprintf("%x", sha256.Sum256(embedded)))
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trust := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(trust, pub, 0600))
	s.grantPublicKeyFile = trust
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "file-stop-owner", TopologyUID: "file-stop-topology", ResourceUID: "file-stop-node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "file-stop-e2e", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	require.NoError(t, conn.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	run := func(command string, body []byte) substrate.ExecResult {
		t.Helper()
		r, err := conn.Exec(ctx, substrate.ExecRequest{Program: command, Shell: substrate.ShellLinux, Stdin: bytes.NewReader(body)}, nil, nil)
		require.NoError(t, err)
		require.True(t, r.ExitCodeKnown)
		require.Zero(t, r.ExitCode, r.Stderr)
		return r
	}
	const root = "/opt/sysbox-file-stop"
	run(`mkdir -p `+root+`/target /etc/sysbox; chmod 700 `+root+` `+root+`/target; cp /etc/sysbox/grant-public-key `+root+`/issuer.pub; touch `+root+`/lost-outcome; printf '["`+root+`/target"]' > /etc/sysbox/install-roots.json; chmod 600 /etc/sysbox/install-roots.json`, nil)
	require.NoError(t, s.CopyToNode(ctx, h, probe, root+"/probe", 0700))
	boot, err := conn.(interface {
		GuestFileBootID(context.Context) (string, error)
	}).GuestFileBootID(ctx)
	require.NoError(t, err)
	body := []byte("persistent-vsock-proof")
	req := substrate.FileInstallRequest{ID: "install", Path: root + "/target/file", Mode: 0600, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	sign := func(r substrate.FileInstallRequest, epoch uint64) controlplane.SignedExecutionGrant {
		digest, err := r.Digest()
		require.NoError(t, err)
		g, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation}, TargetBootID: boot, AgentID: "agent", SessionID: "session", CommandID: "command-" + r.ID, ActionID: r.ID, Attempt: 1, Epoch: epoch, Operation: "guest_file_install", Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, boot, time.Now())
		require.NoError(t, err)
		return g
	}
	g := sign(req, 2)
	call, _ := json.Marshal(guesttask.Call{Version: 1, Operation: "file-install", Grant: &g, FileRequest: &req, FileData: body})
	response := run("env SYSBOX_FILE_FENCE_ROOT="+root+" "+root+"/probe -test.run=^TestFileFenceAcceptanceProbe$", call)
	var lost guesttask.Response
	require.NoError(t, json.Unmarshal([]byte(response.Stdout), &lost))
	require.NotEmpty(t, lost.Error)
	// Move the actual writer's untouched signed journal, including its locks and
	// scope state, into the production helper's trusted journal location.
	run("test ! -e /var/lib/sysbox/guest-tasks && mkdir -p /var/lib/sysbox && mv "+root+"/journal /var/lib/sysbox/guest-tasks", nil)
	digest, err := req.Digest()
	require.NoError(t, err)
	handle := substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: boot, NodeID: h.ID, TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: id.Generation, Epoch: 2}
	stopper, ok := conn.(interface {
		StopFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error)
	})
	require.True(t, ok)
	stopped, err := stopper.StopFileInstall(transport.WithTaskGrant(ctx, req.ID, g), handle)
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallUnknown, stopped.Outcome)
	require.True(t, stopped.Quiescent)
	require.Equal(t, uint64(2), stopped.StoppedEpoch)
	files := conn.(substrate.ConnectionFileInstaller)
	_, _, err = files.InstallFile(transport.WithTaskGrant(ctx, req.ID, g), req, bytes.NewReader(body))
	require.Error(t, err)
	next := req
	next.ID = "successor"
	nextBody := []byte("distinct-successor-payload")
	next.Size = int64(len(nextBody))
	next.SHA256 = fmt.Sprintf("%x", sha256.Sum256(nextBody))
	ng := sign(next, 3)
	nh, committed, err := files.InstallFile(transport.WithTaskGrant(ctx, next.ID, ng), next, bytes.NewReader(nextBody))
	require.NoError(t, err)
	require.Equal(t, substrate.FileInstallCommitted, committed.Outcome)
	again, err := stopper.StopFileInstall(transport.WithTaskGrant(ctx, req.ID, g), handle)
	require.NoError(t, err)
	require.Equal(t, stopped, again)
	observed, err := files.ObserveFileInstall(ctx, nh)
	require.NoError(t, err)
	require.Equal(t, committed, observed)
	actual := run("cat "+next.Path, nil)
	require.Equal(t, string(nextBody), actual.Stdout)
	r := run("cat /var/lib/sysbox/guest-tasks/.installs/install.json", nil)
	var historical guesttask.FileInstallRecord
	require.NoError(t, json.Unmarshal([]byte(r.Stdout), &historical))
	require.False(t, historical.Result.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, historical.Result.Outcome)
	require.Error(t, s.HostFenceDeletionAbsent(ctx, h), "live microVM is not absent")
	require.NoError(t, s.DestroyNode(ctx, h))
	require.NoError(t, s.HostFenceDeletionAbsent(ctx, h))
	t.Log("real KVM/vsock: injected commit-intent unknown preserved, unknown writer stopped, delayed install refused, epoch3 installed, old stop retry preserved successor")
}
