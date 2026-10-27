//go:build e2e

package libvirt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestWindowsQGADurableTaskFaultMatrixE2E(t *testing.T) {
	image := os.Getenv("SYSBOX_WINDOWS_TASK_IMAGE")
	if image == "" {
		t.Skip("requires isolated Windows Server image and current helper")
	}
	helper, native, installer := os.Getenv("SYSBOX_WINDOWS_TASK_HELPER"), os.Getenv("SYSBOX_WINDOWS_TASK_TEST"), os.Getenv("SYSBOX_WINDOWS_TASK_INSTALLER")
	for _, path := range []string{image, helper, native, installer} {
		require.FileExists(t, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("/var/tmp", "sysbox-windows-task-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0755))
	s := New(root)
	testUID := filepath.Base(root)
	node := substrate.OperationIdentity{ActionID: "windows-create", OwnerUID: testUID, TopologyUID: testUID, ResourceUID: testUID + "-node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, node)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "sysbox-windows-task-" + strings.TrimPrefix(filepath.Base(root), "sysbox-windows-task-"), Image: substrate.ArtifactHandle{ID: image, Identity: substrate.ArtifactIdentity{Kind: substrate.ArtifactQCow2, GuestFamily: substrate.GuestFamilyWindows}}, ProviderConfig: &Config{VCPUs: 2, Memory: "4096", MachineType: "q35", NetworkInit: substrate.GuestNetworkInitPreconfigured}})
	require.NoError(t, err)
	preserve := false
	t.Cleanup(func() {
		if preserve {
			return
		}
		if t.Failed() && os.Getenv("SYSBOX_WINDOWS_TASK_KEEP_FAILED") == "1" {
			t.Logf("retained test VM UUID=%s state=%s", h.ID, root)
			return
		}
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		require.NoError(t, s.DestroyNode(cleanup, h))
		require.NoError(t, os.RemoveAll(root))
	})
	require.NoError(t, s.PrepareGuestNetwork(ctx, h))
	require.NoError(t, s.StartNode(ctx, h))
	connection, err := s.Connection(h, nil)
	require.NoError(t, err)
	conn := connection.(*qgaDomainConnection)
	require.NoError(t, conn.WaitReady(ctx, 5*time.Minute))
	run := func(script string) substrate.ExecResult {
		result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, Shell: substrate.ShellNone, OutputLimitBytes: 1 << 20}, nil, nil)
		require.NoError(t, err)
		require.True(t, result.ExitCodeKnown)
		require.Zero(t, result.ExitCode, result.Stderr+result.Stdout)
		return result
	}
	run(`New-Item -ItemType Directory -Force C:\SysboxAcceptance | Out-Null`)
	require.NoError(t, conn.CopyFile(ctx, helper, `C:\SysboxAcceptance\helper.exe`))
	require.NoError(t, conn.CopyFile(ctx, installer, `C:\SysboxAcceptance\install.ps1`))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	helperBytes, err := os.ReadFile(helper)
	require.NoError(t, err)
	install := fmt.Sprintf(`& 'C:\SysboxAcceptance\install.ps1' -HelperPath 'C:\SysboxAcceptance\helper.exe' -HelperSHA256 '%x' -IssuerPublicKeyBase64 '%s' -TopologyUID '%s' -ResourceUID '%s' -Generation 1`, sha256.Sum256(helperBytes), base64.StdEncoding.EncodeToString(pub), node.TopologyUID, node.ResourceUID)
	run(install)
	nativePath := `C:\ProgramData\Sysbox\guest-task\native.test.exe`
	require.NoError(t, conn.CopyFile(ctx, native, nativePath))
	issuerFile := filepath.Join(root, "test-issuer.key")
	require.NoError(t, os.WriteFile(issuerFile, key, 0600))
	require.NoError(t, conn.CopyFile(ctx, issuerFile, `C:\ProgramData\Sysbox\guest-task\test-issuer.key`))
	oldBoot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, oldBoot)
	// Exercise the actual host transport, including recovering a deliberately
	// discarded Start response with the same signed identity.
	qgaRequest := substrate.ExecRequest{Program: "cmd.exe", Args: []string{"/c", "echo qga-durable-output & exit /b 7"}, Shell: substrate.ShellNone, OutputLimitBytes: 1024}
	qgaTask, err := transport.BuildTaskRequest(ctx, "qga-normal", qgaRequest)
	require.NoError(t, err)
	qgaDigest, err := qgaTask.Digest()
	require.NoError(t, err)
	qgaGrant, err := controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: node.TopologyUID, ResourceUID: node.ResourceUID, Generation: 1}, AgentID: "host", SessionID: "session", CommandID: "qga-command", ActionID: qgaTask.ID, Attempt: 1, Epoch: 1, Operation: "guest_task", Digest: qgaDigest, TargetBootID: oldBoot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}, oldBoot, time.Now())
	require.NoError(t, err)
	authorized := transport.WithTaskGrant(ctx, qgaTask.ID, qgaGrant)
	_, err = conn.Start(authorized, qgaRequest)
	require.NoError(t, err)
	qgaHandle, err := conn.Start(authorized, qgaRequest)
	require.NoError(t, err)
	var qgaResult substrate.ExecResult
	require.Eventually(t, func() bool {
		qgaResult, err = conn.ObserveExecution(ctx, qgaHandle)
		return err == nil && qgaResult.RemoteTermination == substrate.RemoteTerminationConfirmed
	}, 30*time.Second, time.Second)
	require.True(t, qgaResult.ExitCodeKnown)
	require.Equal(t, 7, qgaResult.ExitCode)
	qgaOutput, err := conn.ReadExecutionOutput(ctx, qgaHandle, "stdout", 0, 1024)
	require.NoError(t, err)
	require.Contains(t, string(qgaOutput.Data), "qga-durable-output")
	nativeResult := run(`$env:SYSBOX_WINDOWS_TASK_E2E='1'; $env:SYSBOX_WINDOWS_REBOOT_PREPARE='1'; $env:SYSBOX_WINDOWS_TEST_ISSUER='C:\ProgramData\Sysbox\guest-task\test-issuer.key'; & 'C:\ProgramData\Sysbox\guest-task\native.test.exe' '-test.run=^TestWindowsNative' '-test.v' '-test.timeout=3m'; exit $LASTEXITCODE`)
	t.Log(nativeResult.Stdout)
	raw := run(`Get-Content -Raw 'C:\ProgramData\Sysbox\guest-task\reboot-request.json'`).Stdout
	var request guesttask.Request
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(raw)), &request))
	old := request.Grant
	handle := substrate.ExecutionHandle{ID: request.ID, NodeID: h.ID, Generation: node.Generation, TopologyUID: node.TopologyUID, ResourceUID: node.ResourceUID, Digest: old.Grant.Digest, BootID: oldBoot, Epoch: old.Grant.Epoch}
	running, err := conn.ObserveExecution(ctx, handle)
	require.NoError(t, err)
	require.False(t, running.RemoteTermination == substrate.RemoteTerminationConfirmed)
	// Entire guest kernel loses power while the signed process tree is active.
	_, err = virshQuery(ctx, "destroy", hsFrom(h).DomainUUID)
	require.NoError(t, err)
	require.NoError(t, s.StartNode(ctx, h))
	require.NoError(t, conn.WaitReady(ctx, 5*time.Minute))
	newBoot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEqual(t, oldBoot, newBoot)
	result, err := conn.ObserveExecution(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, substrate.RemoteTerminationConfirmed, result.RemoteTermination)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, substrate.ExecStatusUnknown, result.Status)
	execReq := substrate.ExecRequest{Program: request.Args[0], Args: request.Args[1:], Shell: substrate.ShellNone, OutputLimitBytes: request.OutputLimit}
	_, err = conn.Start(transport.WithTaskGrant(ctx, request.ID, old), execReq)
	require.Error(t, err)
	preserve = finishWindowsRebootAcceptance(t, ctx, root, h, conn, request, key, newBoot)
}

func finishWindowsRebootAcceptance(t *testing.T, ctx context.Context, root string, h substrate.NodeHandle, conn *qgaDomainConnection, request guesttask.Request, key ed25519.PrivateKey, newBoot string) bool {
	old := request.Grant
	pub := key.Public().(ed25519.PublicKey)
	execReq := substrate.ExecRequest{Program: request.Args[0], Args: request.Args[1:], Shell: substrate.ShellNone, OutputLimitBytes: request.OutputLimit}
	journalRoot := filepath.Join("/var/lib/sysbox-windows-acceptance-tests", filepath.Base(root))
	require.NoError(t, os.MkdirAll(journalRoot, 0700))
	// The host signs the same generic stop receipt used for Linux guests.
	hostPublic, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostBoot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	var receipt controlplane.SignedExecutionStopReceipt
	dispatcher := &hostsupervisor.Dispatcher{Root: journalRoot, AgentID: "host", HostBootID: strings.TrimSpace(string(hostBoot)), IssuerKey: pub, HostKey: hostKey, Resolve: func(context.Context, hostsupervisor.TargetRef) (hostsupervisor.Target, error) {
		return hostsupervisor.Target{Scope: old.Grant.Scope, AgentID: "host", NodeID: h.ID, Connection: conn}, nil
	}, ReportStop: func(_ context.Context, r controlplane.SignedExecutionStopReceipt) error { receipt = r; return nil }}
	revoked := old.Grant
	revoked.State = controlplane.GrantRevoked
	stop, err := controlplane.SignExecutionCancellation(key, revoked)
	require.NoError(t, err)
	_, err = dispatcher.StopAuthorized(ctx, controlplane.GuestTaskStop{Topology: "windows-lab", Node: "windows-node", Cancellation: stop})
	require.NoError(t, err)
	require.NoError(t, controlplane.VerifyExecutionStopReceipt(hostPublic, receipt))
	require.Equal(t, "unknown", receipt.GuestResult.ResultClass)
	if checkpoint := os.Getenv("SYSBOX_WINDOWS_HOST_CHECKPOINT"); checkpoint != "" {
		// Leave an active signed process for the outer controller to power off
		// the entire execution host, including this libvirt/QEMU instance.
		request.ID = "host-reboot-task"
		request.Grant = controlplane.SignedExecutionGrant{}
		digest, err := request.Digest()
		require.NoError(t, err)
		grant := old.Grant
		grant.ActionID, grant.CommandID, grant.Digest = request.ID, "host-reboot-command", digest
		grant.TargetBootID, grant.Epoch, grant.ExpiresAt = newBoot, grant.Epoch+1, time.Now().Add(4*time.Minute)
		request.Grant, err = controlplane.SignExecutionGrant(key, grant, newBoot, time.Now())
		require.NoError(t, err)
		hostTask, err := conn.Start(transport.WithTaskGrant(ctx, request.ID, request.Grant), execReq)
		require.NoError(t, err)
		active, err := conn.ObserveExecution(ctx, hostTask)
		require.NoError(t, err)
		require.NotEqual(t, substrate.RemoteTerminationConfirmed, active.RemoteTermination)
		proof := windowsHostCheckpoint{Root: root, Node: h, Provider: hsFrom(h), Task: hostTask, Request: request, HostBoot: strings.TrimSpace(string(hostBoot))}
		raw, err := json.Marshal(proof)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(checkpoint, raw, 0600))
		t.Log("WINDOWS_HOST_FENCE_READY")
		return true
	}
	t.Log("real Windows/QGA: native job normal/lost-response/supervisor-crash and cancellation matrix, whole guest reboot, old boot rejection and signed unknown host stop receipt passed")
	return false
}

type windowsHostCheckpoint struct {
	Root     string                    `json:"root"`
	Provider *HandleState              `json:"provider"`
	Node     substrate.NodeHandle      `json:"node"`
	Task     substrate.ExecutionHandle `json:"task"`
	Request  guesttask.Request         `json:"request"`
	HostBoot string                    `json:"host_boot"`
}

func TestWindowsQGAWholeHostRestartRecoveryE2E(t *testing.T) {
	path := os.Getenv("SYSBOX_WINDOWS_HOST_CHECKPOINT")
	if path == "" {
		t.Skip("requires retained isolated host checkpoint")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var proof windowsHostCheckpoint
	require.NoError(t, json.Unmarshal(raw, &proof))
	require.NotNil(t, proof.Provider)
	proof.Node.Provider = proof.Provider
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	require.NotEqual(t, proof.HostBoot, strings.TrimSpace(string(boot)))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	s := New(proof.Root)
	require.NoError(t, s.StartNode(ctx, proof.Node))
	connection, err := s.Connection(proof.Node, nil)
	require.NoError(t, err)
	conn := connection.(*qgaDomainConnection)
	require.NoError(t, conn.WaitReady(ctx, 6*time.Minute))
	newBoot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEqual(t, proof.Task.BootID, newBoot)
	result, err := conn.ObserveExecution(ctx, proof.Task)
	require.NoError(t, err)
	require.Equal(t, substrate.RemoteTerminationConfirmed, result.RemoteTermination)
	require.Equal(t, substrate.ExecStatusUnknown, result.Status)
	require.False(t, result.ExitCodeKnown)
	request := substrate.ExecRequest{Program: proof.Request.Args[0], Args: proof.Request.Args[1:], Shell: substrate.ShellNone, OutputLimitBytes: proof.Request.OutputLimit}
	_, err = conn.Start(transport.WithTaskGrant(ctx, proof.Request.ID, proof.Request.Grant), request)
	require.Error(t, err)
	// Retain the historical unknown record and disk as evidence; stop only this
	// exact test domain. Destruction/GC of retained evidence is a separate action.
	_, err = virshQuery(ctx, "destroy", hsFrom(proof.Node).DomainUUID)
	require.NoError(t, err)
	t.Log("whole execution host power loss: changed host/guest boots, retained unknown exit, confirmed stop, late old-boot rejection passed")
}

func TestWindowsHostCheckpointRetainsTypedProvider(t *testing.T) {
	identity := &substrate.OperationIdentity{TopologyUID: "owner", ResourceUID: "node", Generation: 3}
	provider := &HandleState{DomainUUID: "immutable-uuid", GuestFamily: substrate.GuestFamilyWindows, Identity: identity}
	original := windowsHostCheckpoint{Provider: provider, Node: substrate.NodeHandle{ID: "immutable-uuid", Generation: 3, Provider: provider}}
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded windowsHostCheckpoint
	require.NoError(t, json.Unmarshal(raw, &decoded))
	decoded.Node.Provider = decoded.Provider
	require.Equal(t, provider, hsFrom(decoded.Node))
	require.Equal(t, int64(3), decoded.Node.Generation)
}

// Resume only a retained isolated matrix after guest reboot; this never replays
// its old request and retains its original issuer and canonical resource scope.
func TestWindowsRetainedGuestRebootCompletionE2E(t *testing.T) {
	root := os.Getenv("SYSBOX_WINDOWS_RETAINED_ROOT")
	if root == "" {
		t.Skip("requires retained isolated Windows matrix")
	}
	require.Equal(t, "/var/tmp", filepath.Dir(root))
	require.True(t, strings.HasPrefix(filepath.Base(root), "sysbox-windows-task-"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	uid := filepath.Base(root)
	node := substrate.OperationIdentity{ActionID: "windows-create", OwnerUID: uid, TopologyUID: uid, ResourceUID: uid + "-node", Generation: 1}
	s := New(root)
	h, err := s.DiscoverManagedNode(ctx, node, uid)
	require.NoError(t, err)
	connection, err := s.Connection(h, nil)
	require.NoError(t, err)
	conn := connection.(*qgaDomainConnection)
	require.NoError(t, conn.WaitReady(ctx, time.Minute))
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", `Get-Content -Raw C:\ProgramData\Sysbox\guest-task\reboot-request.json`}, Shell: substrate.ShellNone, OutputLimitBytes: 1 << 20}, nil, nil)
	require.NoError(t, err)
	require.True(t, result.ExitCodeKnown)
	require.Zero(t, result.ExitCode)
	var request guesttask.Request
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &request))
	newBoot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEqual(t, request.Grant.Grant.TargetBootID, newBoot)
	handle := substrate.ExecutionHandle{ID: request.ID, NodeID: h.ID, Generation: h.Generation, TopologyUID: uid, ResourceUID: uid + "-node", Digest: request.Grant.Grant.Digest, BootID: request.Grant.Grant.TargetBootID, Epoch: request.Grant.Grant.Epoch}
	observed, err := conn.ObserveExecution(ctx, handle)
	require.NoError(t, err)
	require.Equal(t, substrate.RemoteTerminationConfirmed, observed.RemoteTermination)
	require.False(t, observed.ExitCodeKnown)
	require.Equal(t, substrate.ExecStatusUnknown, observed.Status)
	key, err := os.ReadFile(filepath.Join(root, "test-issuer.key"))
	require.NoError(t, err)
	require.Len(t, key, ed25519.PrivateKeySize)
	require.True(t, finishWindowsRebootAcceptance(t, ctx, root, h, conn, request, ed25519.PrivateKey(key), newBoot))
}
