package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/stretchr/testify/require"
)

func TestRealPolicyBuiltinTaskLifecycle(t *testing.T) {
	if os.Getenv("SYSBOX_POLICY_TASK_E2E") != "1" {
		t.Skip("requires disposable root guest with nft and writable cgroup")
	}
	helper := os.Getenv("SYSBOX_POLICY_HELPER")
	require.NotEmpty(t, helper)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := policyTaskFixture(t)
	p, err := PolicyBuiltin(req)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll("/etc/sysbox", 0700))
	require.NoError(t, os.WriteFile("/etc/sysbox/grant-public-key", public, 0600))
	nodeRaw, err := json.Marshal(p.Node)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("/etc/sysbox/node-identity.json", nodeRaw, 0600))
	cg := "/sys/fs/cgroup/sysbox-tasks"
	require.NoError(t, os.MkdirAll(cg, 0700))
	root := "/var/lib/sysbox/guest-tasks"
	e := &Engine{Root: root, CgroupRoot: cg, PublicKey: public, WorkerBinary: helper, NodeIdentity: &p.Node}
	boot, err := BootID()
	require.NoError(t, err)
	sign := func(r *Request, epoch uint64) {
		digest, err := r.Digest()
		require.NoError(t, err)
		g := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: p.Node.TopologyUID, ResourceUID: p.Node.ResourceUID, Generation: p.Node.Generation}, AgentID: "host", SessionID: "process", ActionID: r.ID, CommandID: "run", Attempt: 1, Epoch: epoch, Operation: "guest_task", Digest: digest, TargetBootID: boot, State: controlplane.GrantActive, ExpiresAt: time.Now().Add(time.Minute)}
		r.Grant, err = controlplane.SignExecutionGrant(private, g, boot, time.Now())
		require.NoError(t, err)
	}
	await := func(r Request) Record {
		digest, err := r.Digest()
		require.NoError(t, err)
		var record Record
		require.Eventually(t, func() bool {
			record, err = e.Observe(context.Background(), r.ID, digest)
			return err == nil && record.TerminationConfirmed
		}, 10*time.Second, 25*time.Millisecond)
		if !record.ExitCodeKnown || record.ExitCode != 0 {
			raw, _ := os.ReadFile(filepath.Join(root, r.ID, "stderr"))
			t.Fatalf("task failed: %+v stderr=%s", record, raw)
		}
		return record
	}
	sign(&req, 1)
	first, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	result := await(req)
	require.True(t, result.TerminationConfirmed)
	e = &Engine{Root: root, CgroupRoot: cg, PublicKey: public, WorkerBinary: helper, NodeIdentity: &p.Node}
	repeated, err := e.Start(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first.ID, repeated.ID)
	require.Equal(t, result.PID, repeated.PID)
	engine := &network.GuestPolicyEngine{Root: "/var/lib/sysbox/guest-policies"}
	query := p
	query.Operation = "observe"
	_, err = engine.Execute(context.Background(), query)
	require.NoError(t, err)
	// A real second task changes policy generation using one nft transaction.
	p.Identity.Generation++
	p.Spec.DefaultInput = "accept"
	req.ID = "policy-next"
	req.Input, err = json.Marshal(p)
	require.NoError(t, err)
	sign(&req, 2)
	_, err = e.Start(context.Background(), req)
	require.NoError(t, err)
	await(req)
	old := query
	old.Operation = "delete"
	_, err = engine.Execute(context.Background(), old)
	require.Error(t, err)
	// Delete is a durable task too; replay cannot recreate the retired policy.
	p.Operation = "delete"
	req.ID = "policy-delete"
	req.Input, err = json.Marshal(p)
	require.NoError(t, err)
	sign(&req, 3)
	_, err = e.Start(context.Background(), req)
	require.NoError(t, err)
	await(req)
	p.Operation = "apply"
	_, err = engine.Execute(context.Background(), p)
	require.Error(t, err)
	output, err := exec.Command("/usr/sbin/nft", "list", "ruleset").CombinedOutput()
	require.NoError(t, err, string(output))
	require.NotContains(t, string(output), "sysbox-owner=lab/fw")
	t.Log("real root builtin launch, signed admission, cgroup stop, lost reply/cold reconstruction, atomic replacement, delete tombstone and delayed generation passed")
}
