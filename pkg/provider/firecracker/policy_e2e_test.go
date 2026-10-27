//go:build e2e

package firecracker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestFirecrackerDurablePolicyFaultMatrixE2E(t *testing.T) {
	tools := os.Getenv("SYSBOX_POLICY_NFT_ARCHIVE")
	if tools == "" {
		t.Skip("requires isolated KVM and pinned nft userspace archive")
	}
	bin, kernel, rootfs := realFirecrackerPaths(t)
	vmRoot := t.TempDir()
	if persistent := os.Getenv("SYSBOX_POLICY_HOST_FENCE_ROOT"); persistent != "" {
		vmRoot = filepath.Join(persistent, "vms")
		require.NoError(t, os.MkdirAll(vmRoot, 0700))
	}
	s := realFirecracker(t, kernel, bin, vmRoot)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trust := filepath.Join(t.TempDir(), "issuer.pub")
	require.NoError(t, os.WriteFile(trust, pub, 0600))
	s.grantPublicKeyFile = trust
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	node := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, node)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "policy-vm", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel, AllowDirect: true}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	require.NoError(t, s.StartNode(ctx, h))
	connect := func() *transport.VsockConnection {
		c, err := s.Connection(h, nil)
		require.NoError(t, err)
		v := c.(*transport.VsockConnection)
		require.NoError(t, v.WaitReady(ctx, 45*time.Second))
		return v
	}
	conn := connect()
	run := func(command string) {
		r, err := conn.Exec(ctx, substrate.ExecRequest{Program: command, Shell: substrate.ShellLinux}, nil, nil)
		require.NoError(t, err)
		require.True(t, r.ExitCodeKnown)
		require.Zero(t, r.ExitCode, r.Stderr)
	}
	require.NoError(t, conn.CopyFile(ctx, tools, "/tmp/policy-nft.tar"))
	run("mkdir -p /opt/sysbox-policy-tools && tar xf /tmp/policy-nft.tar -C /opt/sysbox-policy-tools && mkdir -p /usr/sbin && printf '%s\\n' '#!/bin/sh' 'if test -e /var/lib/sysbox/policy-test-pause; then touch /var/lib/sysbox/policy-test-entered; sleep 600; fi' 'if test \"$1\" = -f && test -e /var/lib/sysbox/policy-test-fail; then rm /var/lib/sysbox/policy-test-fail; exit 42; fi' 'exec /opt/sysbox-policy-tools/ld-linux-x86-64.so.2 --library-path /opt/sysbox-policy-tools /opt/sysbox-policy-tools/nft \"$@\"' > /usr/sbin/nft && chmod 755 /usr/sbin/nft && /usr/sbin/nft list ruleset && sync")
	boot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	raw, err := json.Marshal(firecrackerPolicyTarget{ContainerID: h.ID, ProviderState: mustPolicyJSON(t, h.Provider)})
	require.NoError(t, err)
	policy := node
	policy.ActionID = "policy"
	policy.ResourceUID = "firewall"
	target := driver.PolicyTarget{Resource: "sysbox_node.vm", Identity: node, Policy: policy, State: raw}
	spec := driver.RulesetSpec{Owner: "lab/firewall", Family: driver.FamilyIPv4, DefaultInput: driver.VerdictDrop, DefaultForward: driver.VerdictDrop, DefaultOutput: driver.VerdictAccept}
	var mu sync.Mutex
	grants := map[string]controlplane.SignedExecutionGrant{}
	var epoch uint64
	withHost := func(base context.Context) context.Context {
		return transport.WithTaskStartHandler(base, func(callCtx context.Context, id string, r substrate.ExecRequest, identity substrate.OperationIdentity) (substrate.ExecutionHandle, error) {
			task, err := transport.BuildTaskRequest(callCtx, id, r)
			if err != nil {
				return substrate.ExecutionHandle{}, err
			}
			digest, err := task.Digest()
			if err != nil {
				return substrate.ExecutionHandle{}, err
			}
			mu.Lock()
			g, ok := grants[id]
			if !ok {
				epoch++
				g, err = controlplane.SignExecutionGrant(key, controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: node.TopologyUID, ResourceUID: node.ResourceUID, Generation: node.Generation}, AgentID: "host", SessionID: "process", ActionID: id, CommandID: "run", Attempt: 1, Epoch: epoch, Operation: "guest_task", Digest: digest, TargetBootID: boot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(2 * time.Minute)}, boot, time.Now())
				grants[id] = g
			}
			mu.Unlock()
			if err != nil {
				return substrate.ExecutionHandle{}, err
			}
			r.Stdin = bytes.NewReader(task.Input)
			handle, err := conn.Start(transport.WithTaskGrant(callCtx, id, g), r)
			if err == nil {
				handle.NodeID = "vm"
			}
			return handle, err
		})
	}
	first, err := s.ApplyRuleset(withHost(ctx), target, spec)
	require.NoError(t, err)
	require.NotEmpty(t, first.Digest)
	// Drop the result and reconstruct the provider: stable task and policy state
	// must be observed, not applied again with an unrelated identity.
	cold := realFirecracker(t, kernel, bin, s.rootfsDir)
	next, err := cold.ApplyRuleset(withHost(ctx), target, spec)
	require.NoError(t, err)
	require.Equal(t, first, next)
	old := target
	target.Policy.Generation++
	target.Policy.ActionID = "replace"
	spec.DefaultInput = driver.VerdictAccept
	_, err = cold.ApplyRuleset(withHost(ctx), target, spec)
	require.NoError(t, err)
	require.Error(t, cold.DeleteRuleset(withHost(ctx), old, spec.Owner))
	applied := target
	// Block a real guest policy task in nft, then stop the entire guest kernel.
	run("touch /var/lib/sysbox/policy-test-pause")
	target.Policy.Generation++
	target.Policy.ActionID = "interrupted"
	spec.DefaultInput = driver.VerdictDrop
	pendingCtx, stopPending := context.WithCancel(withHost(ctx))
	finished := make(chan error, 1)
	go func() { _, err := cold.ApplyRuleset(pendingCtx, target, spec); finished <- err }()
	require.Eventually(t, func() bool {
		r, err := conn.Exec(ctx, substrate.ExecRequest{Program: "test -e /var/lib/sysbox/policy-test-entered", Shell: substrate.ShellLinux}, nil, nil)
		return err == nil && r.ExitCodeKnown && r.ExitCode == 0
	}, 15*time.Second, 50*time.Millisecond)
	if persistent := os.Getenv("SYSBOX_POLICY_HOST_FENCE_ROOT"); persistent != "" {
		run("sync")
		hostBoot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		require.NoError(t, err)
		mu.Lock()
		var pending controlplane.SignedExecutionGrant
		for _, g := range grants {
			if g.Grant.Epoch > pending.Grant.Epoch {
				pending = g
			}
		}
		mu.Unlock()
		evidence := policyHostFenceEvidence{HostBoot: strings.TrimSpace(string(hostBoot)), GuestBoot: boot, PublicKey: pub, Grant: pending, Disk: filepath.Join(h.Provider.(*HandleState).VMDir, "rootfs.ext4")}
		raw, err := json.Marshal(evidence)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(persistent, "ready.json"), raw, 0600))
		require.NoError(t, exec.Command("sync").Run())
		fmt.Println("POLICY_HOST_FENCE_READY")
		<-ctx.Done()
		t.Fatal("external host power fence was not performed")
	}
	require.NoError(t, s.StopNode(ctx, h))
	stopPending()
	select {
	case err := <-finished:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("policy waiter did not terminate")
	}
	require.Eventually(t, func() bool { o, err := s.ObserveNode(ctx, h); return err == nil && !o.Running }, 10*time.Second, 50*time.Millisecond)
	require.NoError(t, s.StartNode(ctx, h))
	conn = connect()
	newBoot, err := conn.GuestTaskBootID(ctx)
	require.NoError(t, err)
	require.NotEqual(t, boot, newBoot)
	run("rm -f /var/lib/sysbox/policy-test-pause")
	mu.Lock()
	var oldGrant controlplane.SignedExecutionGrant
	for _, g := range grants {
		oldGrant = g
		break
	}
	mu.Unlock()
	_, err = conn.Start(transport.WithTaskGrant(ctx, oldGrant.Grant.ActionID, oldGrant), substrate.ExecRequest{Program: guesttask.PolicyProgram, Shell: substrate.ShellNone, Stdin: bytes.NewReader([]byte("{}")), OutputLimitBytes: 4096})
	require.Error(t, err)
	// A new guest boot cannot turn interrupted work into success. A fresh,
	// higher-generation policy is admitted only with current-boot authority.
	boot = newBoot
	// Same policy generation gets a new execution identity on the new boot.
	restoredSpec := spec
	restoredSpec.DefaultInput = driver.VerdictAccept
	_, err = cold.ApplyRuleset(withHost(ctx), applied, restoredSpec)
	require.NoError(t, err)
	target.Policy.Generation++
	target.Policy.ActionID = "post-reboot"
	_, err = cold.ApplyRuleset(withHost(ctx), target, spec)
	require.NoError(t, err)
	run("touch /var/lib/sysbox/policy-test-fail && sync")
	require.Error(t, cold.DeleteRuleset(withHost(ctx), target, spec.Owner))
	require.Error(t, cold.DeleteRuleset(withHost(ctx), target, spec.Owner), "same attempt must retain failed result")
	recovery := transport.WithParentExecutionGrant(ctx, controlplane.SignedExecutionGrant{Grant: controlplane.ExecutionGrant{CommandID: "recovery-run", Attempt: 1, Epoch: 100}})
	require.NoError(t, cold.DeleteRuleset(withHost(recovery), target, spec.Owner))
	_, err = cold.ObserveRuleset(ctx, target, spec.Owner)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	_, err = cold.ApplyRuleset(withHost(ctx), old, spec)
	require.Error(t, err)
	t.Log(fmt.Sprintf("normal/lost-response/cold-host/whole-guest-reboot/stale-policy passed; boot %s -> %s", oldGrant.BootID, newBoot))
}
func mustPolicyJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

type policyHostFenceEvidence struct {
	HostBoot  string                            `json:"host_boot"`
	GuestBoot string                            `json:"guest_boot"`
	PublicKey []byte                            `json:"public_key"`
	Grant     controlplane.SignedExecutionGrant `json:"grant"`
	Disk      string                            `json:"disk"`
}

func TestFirecrackerPolicyHostRebootEvidenceE2E(t *testing.T) {
	root := os.Getenv("SYSBOX_POLICY_HOST_FENCE_ROOT")
	if root == "" {
		t.Skip("requires external isolated-host power fence")
	}
	raw, err := os.ReadFile(filepath.Join(root, "ready.json"))
	require.NoError(t, err)
	var evidence policyHostFenceEvidence
	require.NoError(t, json.Unmarshal(raw, &evidence))
	now, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	require.NotEqual(t, evidence.HostBoot, strings.TrimSpace(string(now)), "whole execution host must have rebooted")
	require.NoError(t, controlplane.VerifyExecutionGrant(evidence.PublicKey, evidence.Grant, evidence.GuestBoot, evidence.Grant.IssuedAt))
	require.Regexp(t, `^run-task-[a-f0-9]{64}$`, evidence.Grant.Grant.ActionID)
	cmd := exec.Command("debugfs", "-R", "cat /var/lib/sysbox/guest-tasks/"+evidence.Grant.Grant.ActionID+"/record.json", evidence.Disk)
	out, err := cmd.Output()
	require.NoError(t, err)
	var record guesttask.Record
	require.NoError(t, json.Unmarshal(out, &record))
	require.Equal(t, evidence.Grant.Grant.ActionID, record.ID)
	require.Equal(t, evidence.Grant.Grant.Digest, record.Digest)
	require.Equal(t, evidence.GuestBoot, record.BootID)
	require.False(t, record.TerminationConfirmed, "historical guest task must not be relabeled successful after power loss")
	require.False(t, record.ExitCodeKnown)
	require.Contains(t, []string{"prepared", "starting", "running"}, record.Status)
	require.Error(t, controlplane.VerifyExecutionGrant(evidence.PublicKey, evidence.Grant, strings.TrimSpace(string(now)), time.Now()))
	t.Log("whole execution VM reboot proved; signed interrupted guest task retained without invented success; old boot authority rejected; disk retained for read-only recovery")
}
