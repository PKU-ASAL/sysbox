package hostsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type fileFenceSSHConnection struct {
	root, session string
	late          <-chan struct{}
}

func (c fileFenceSSHConnection) Exec(ctx context.Context, r substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	command := exec.CommandContext(ctx, "/usr/bin/python3", "../../scripts/acceptance/file_reboot.py", "call", "--root", c.root, "--session", c.session)
	raw, err := io.ReadAll(r.Stdin)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	var call struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return substrate.ExecResult{}, err
	}
	command.Stdin = bytes.NewReader(raw)
	var out, stderr bytes.Buffer
	command.Stdout = &out
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if call.Operation == "file-install" && c.late != nil {
			<-c.late
		}
		return substrate.ExecResult{}, fmt.Errorf("SSH file probe: %w: %s", err, stderr.String())
	}
	return substrate.ExecResult{ExitCodeKnown: true, Stdout: out.String()}, nil
}
func (fileFenceSSHConnection) CopyTo(context.Context, string, string, uint32) error {
	return substrate.ErrNotSupported
}
func (fileFenceSSHConnection) Probe(context.Context) error { return nil }

type fileFenceProvider struct {
	*transport.TaskClient
	fileFenceSSHConnection
}

func TestFileInstallRealGuestRebootUnknown(t *testing.T) {
	lab := os.Getenv("SYSBOX_GC_FENCE_LAB")
	probe := os.Getenv("SYSBOX_FILE_FENCE_PROBE")
	if lab == "" || probe == "" {
		t.Skip("requires enrolled disposable VM and current static guesttask test probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	t.Cleanup(cancel)
	sessionRaw, err := exec.Command("cat", "/proc/sys/kernel/random/uuid").Output()
	require.NoError(t, err)
	session := strings.TrimSpace(string(sessionRaw))
	run := func(operation string, extra ...string) (string, error) {
		args := append([]string{"../../scripts/acceptance/file_reboot.py", operation, "--root", lab, "--session", session}, extra...)
		raw, err := exec.CommandContext(ctx, "/usr/bin/python3", args...).CombinedOutput()
		return string(raw), err
	}
	d, ref, req, g, _ := scopedFileFixture(t, "guest-tmpfs")
	pub, issuer, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	d.IssuerKey = pub
	keyPath := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyPath, d.IssuerKey, 0600))
	output, err := run("setup", "--probe", probe, "--public-key", keyPath)
	require.NoError(t, err, output)
	t.Cleanup(func() {
		out, err := run("cleanup")
		if err != nil {
			t.Errorf("exact fixture cleanup: %v: %s", err, out)
		}
	})
	var fixture struct {
		Target string `json:"target"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &fixture))
	req.Path = fixture.Target

	late := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(late)
		}
	})
	base := fileFenceSSHConnection{root: lab, session: session, late: late}
	conn := fileFenceProvider{TaskClient: transport.NewTaskClient(base, "node", 1), fileFenceSSHConnection: base}
	boot, err := conn.GuestFileBootID(ctx)
	require.NoError(t, err)
	hostBoot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	d.HostBootID = strings.TrimSpace(string(hostBoot))
	digest, err := req.Digest()
	require.NoError(t, err)
	grant := g.Grant
	grant.Digest = digest
	grant.TargetBootID = boot
	grant.ExpiresAt = time.Now().Add(4 * time.Minute)
	g, err = controlplane.SignExecutionGrant(issuer, grant, boot, time.Now())
	require.NoError(t, err)
	d.Resolve = func(context.Context, TargetRef) (Target, error) {
		return Target{Scope: ref.Scope, AgentID: d.AgentID, NodeID: "node", Connection: conn}, nil
	}
	var receipt controlplane.SignedExecutionStopReceipt
	reports := 0
	d.ReportStop = func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error {
		receipt = r
		reports++
		return nil
	}
	done := make(chan error, 1)
	go func() { _, _, err := d.Install(ctx, ref, req, g); done <- err }()
	require.Eventually(t, func() bool { out, err := run("ready"); return err == nil && strings.TrimSpace(out) == "prepared" }, 30*time.Second, 250*time.Millisecond)
	output, err = run("fence")
	require.NoError(t, err, output)
	t.Log(output)
	h := substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: boot, NodeID: ref.Node, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: g.Grant.Epoch}
	result, err := d.Cancel(ctx, ref, h)
	require.NoError(t, err)
	require.True(t, result.Quiescent)
	require.Equal(t, substrate.FileInstallUnknown, result.Outcome)
	require.NotEmpty(t, receipt.ObservedGuestBootID)
	require.NotEqual(t, boot, receipt.ObservedGuestBootID)
	require.NoError(t, controlplane.VerifyExecutionStopReceipt(d.HostKey.Public().(ed25519.PublicKey), receipt))
	// The old real SSH call returns only after the durable unknown receipt exists.
	close(late)
	released = true
	require.NoError(t, <-done, "late RPC must return the already durable unknown result")
	cold := *d
	again, err := cold.Observe(ctx, ref, h)
	require.NoError(t, err)
	require.Equal(t, result, again)
	record, err := cold.loadFile(h)
	require.NoError(t, err)
	require.Equal(t, receipt, *record.Receipt)
	require.Equal(t, 1, reports)
	t.Logf("SSH installer paused prepared on real tmpfs; boot %s -> %s; late old callback preserved unknown receipt", boot, receipt.ObservedGuestBootID)
}

func TestSSHStopRealGuestProtocol(t *testing.T) {
	lab, probe := os.Getenv("SYSBOX_GC_FENCE_LAB"), os.Getenv("SYSBOX_FILE_FENCE_PROBE")
	if lab == "" || probe == "" {
		t.Skip("requires enrolled disposable VM and current guesttask test probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	raw, err := exec.Command("cat", "/proc/sys/kernel/random/uuid").Output()
	require.NoError(t, err)
	session := strings.TrimSpace(string(raw))
	run := func(operation string, extra ...string) (string, error) {
		args := append([]string{"../../scripts/acceptance/file_reboot.py", operation, "--root", lab, "--session", session}, extra...)
		out, err := exec.CommandContext(ctx, "/usr/bin/python3", args...).CombinedOutput()
		return string(out), err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(keyPath, pub, 0600))
	out, err := run("setup", "--probe", probe, "--public-key", keyPath)
	require.NoError(t, err, out)
	t.Cleanup(func() { out, err := run("cleanup"); require.NoError(t, err, out) })
	base := fileFenceSSHConnection{root: lab, session: session}
	conn := transport.NewTaskClient(base, "provider-node", 1)
	boot, err := conn.GuestFileBootID(ctx)
	require.NoError(t, err)
	req := substrate.SSHAccessMutation{ID: "obsolete-ssh", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "acceptance"}}
	grantFor := func(req substrate.SSHAccessMutation, epoch uint64) controlplane.SignedExecutionGrant {
		digest, err := req.Digest()
		require.NoError(t, err)
		op := "guest_ssh_access_apply"
		if req.Delete {
			op = "guest_ssh_access_delete"
		}
		g, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: session, ResourceUID: "node", Generation: 1}, TargetBootID: boot, AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: req.ID, Attempt: 1, Epoch: epoch, ParentEpoch: 1, Operation: op, Digest: digest, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, boot, time.Now())
		require.NoError(t, err)
		return g
	}
	grant := grantFor(req, 2)
	stopped, err := conn.StopSSHAccess(transport.WithTaskGrant(ctx, req.ID, grant), req)
	require.NoError(t, err)
	require.Equal(t, "unknown", stopped.Outcome)
	require.True(t, stopped.Quiescent)
	_, err = conn.MutateSSHAccess(transport.WithTaskGrant(ctx, req.ID, grant), req)
	require.Error(t, err)
	publicKey, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	next := substrate.SSHAccessMutation{ID: "fresh-ssh", Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "acceptance", AuthorizedKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))}}}
	nextGrant := grantFor(next, 3)
	created, err := conn.MutateSSHAccess(transport.WithTaskGrant(ctx, next.ID, nextGrant), next)
	require.NoError(t, err)
	require.True(t, created.Present)
	again, err := conn.StopSSHAccess(transport.WithTaskGrant(ctx, req.ID, grant), req)
	require.NoError(t, err)
	require.Equal(t, stopped, again)
	found, err := conn.InspectSSHAccess(ctx, created.Handle)
	require.NoError(t, err)
	require.Equal(t, "present", found.Status)
	t.Logf("real SSH/helper: stopped unknown original epoch2, rejected delayed mutation, admitted epoch3 and preserved successor on old-stop retry; guest boot %s", boot)
}
