//go:build e2e

package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerVsockWholeGuestRebootStopE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trust := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(trust, public, 0600))
	s.grantPublicKeyFile = trust
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "reboot-owner", TopologyUID: "reboot-owner", ResourceUID: "vm", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "vsock-reboot", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	require.NoError(t, conn.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	oldBoot, err := conn.(interface {
		GuestTaskBootID(context.Context) (string, error)
	}).GuestTaskBootID(ctx)
	require.NoError(t, err)
	request := substrate.ExecRequest{Program: "sleep 600 & wait", Shell: substrate.ShellLinux}
	task, err := transport.BuildTaskRequest(ctx, "old-task", request)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: id.TopologyUID, ResourceUID: id.ResourceUID, Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "old-command", ActionID: task.ID, Attempt: 1, Epoch: 2, Operation: "guest_task", Digest: digest, TargetBootID: oldBoot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(2 * time.Minute)}
	signed, err := controlplane.SignExecutionGrant(private, grant, oldBoot, time.Now())
	require.NoError(t, err)
	oldHandle, err := conn.(substrate.ConnectionStarter).Start(transport.WithTaskGrant(ctx, task.ID, signed), request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, e := conn.(substrate.ConnectionExecutionObserver).ObserveExecution(ctx, oldHandle)
		return e == nil && r.Status == substrate.ExecStatusRunning
	}, 10*time.Second, 50*time.Millisecond)
	// Stop the entire microVM process, not its daemon/helper. The guest kernel
	// and running task disappear; the generation's persistent disk is retained.
	require.NoError(t, s.StopNode(ctx, h))
	require.Eventually(t, func() bool {
		o, e := s.ObserveNode(ctx, h)
		return e == nil && !o.Running && o.Status == substrate.NodeStatusExited
	}, 10*time.Second, 50*time.Millisecond)
	require.NoError(t, s.StartNode(ctx, h))
	cold, err := s.Connection(h, nil)
	require.NoError(t, err)
	require.NoError(t, cold.(substrate.ConnectionWaiter).WaitReady(ctx, 30*time.Second))
	newBoot, err := cold.(interface {
		GuestTaskBootID(context.Context) (string, error)
	}).GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEqual(t, oldBoot, newBoot)
	_, err = cold.(substrate.ConnectionStarter).Start(transport.WithTaskGrant(ctx, task.ID, signed), request)
	require.Error(t, err, "old boot signature must not replay")
	hostPublic, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostBoot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	var receipt controlplane.SignedExecutionStopReceipt
	ref := hostsupervisor.TargetRef{Topology: "lab", Node: "vm", Scope: grant.Scope}
	dispatcher := &hostsupervisor.Dispatcher{Root: t.TempDir(), AgentID: "agent", HostBootID: strings.TrimSpace(string(hostBoot)), IssuerKey: public, HostKey: hostPrivate,
		Resolve: func(context.Context, hostsupervisor.TargetRef) (hostsupervisor.Target, error) {
			return hostsupervisor.Target{Scope: ref.Scope, AgentID: "agent", NodeID: h.ID, Connection: cold}, nil
		},
		ReportStop: func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error { receipt = r; return nil }}
	require.NoError(t, os.Chmod(dispatcher.Root, 0700))
	revoked := grant
	revoked.State = controlplane.GrantRevoked
	cancellation, err := controlplane.SignExecutionCancellation(private, revoked)
	require.NoError(t, err)
	stop := controlplane.GuestTaskStop{Topology: ref.Topology, Node: ref.Node, Cancellation: cancellation}
	record, err := dispatcher.StopAuthorized(ctx, stop)
	require.NoError(t, err)
	require.NoError(t, controlplane.VerifyExecutionStopReceipt(hostPublic, receipt))
	require.Equal(t, newBoot, receipt.ObservedGuestBootID)
	require.Equal(t, "unknown", receipt.GuestResult.ResultClass)
	require.False(t, receipt.GuestResult.ExitCodeKnown)
	require.True(t, record.Reported)
	nextRequest := substrate.ExecRequest{Program: "printf successor", Shell: substrate.ShellLinux}
	nextTask, err := transport.BuildTaskRequest(ctx, "successor", nextRequest)
	require.NoError(t, err)
	next := grant
	next.ActionID = nextTask.ID
	next.CommandID = "next-command"
	next.Epoch++
	next.TargetBootID = newBoot
	next.Digest, err = nextTask.Digest()
	require.NoError(t, err)
	nextSigned, err := controlplane.SignExecutionGrant(private, next, newBoot, time.Now())
	require.NoError(t, err)
	nextHandle, err := cold.(substrate.ConnectionStarter).Start(transport.WithTaskGrant(ctx, nextTask.ID, nextSigned), nextRequest)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, e := cold.(substrate.ConnectionExecutionObserver).ObserveExecution(ctx, nextHandle)
		return e == nil && r.ExitCodeKnown && r.ExitCode == 0
	}, 10*time.Second, 50*time.Millisecond)
	restarted := *dispatcher
	again, err := restarted.StopAuthorized(ctx, stop)
	require.NoError(t, err)
	require.Equal(t, record.Result, again.Result)
	output, err := cold.(substrate.ConnectionExecutionOutput).ReadExecutionOutput(ctx, nextHandle, "stdout", 0, 100)
	require.NoError(t, err)
	require.Equal(t, "successor", string(output.Data))
	t.Logf("whole guest kernel reboot %s -> %s; real old task stopped, late old signature refused, cold signed unknown stop preserved, successor completed", oldBoot, newBoot)
}
