package commands

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestHostFenceAttestationPinsAdmissionAndRejectsForeignEvidence(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "run", LabID: "isolated", ControllerKey: pub, JournalSHA256: strings.Repeat("a", 64), Grant: controlplane.ExecutionGrant{AgentID: "agent", SessionID: "session", CommandID: "command", ActionID: "run", Attempt: 1, Epoch: 1, Operation: "run_destroy", Digest: strings.Repeat("b", 64), Scope: controlplane.ExecutionScope{TopologyUID: "owner"}, TargetBootID: "11111111-1111-4111-8111-111111111111", ExpiresAt: time.Now().UTC().Add(time.Minute), State: controlplane.GrantActive}}
	digest, err := req.Digest()
	require.NoError(t, err)
	dir := t.TempDir()
	admission := filepath.Join(dir, "request.json")
	report := filepath.Join(dir, "evidence.json")
	keyPath := filepath.Join(dir, "key")
	save := func(path string, v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
	save(admission, persistence.HostFenceRecord{Request: req, BarrierID: "fence-fence", AcceptedAt: time.Now().UTC().Add(-time.Minute)})
	require.NoError(t, os.WriteFile(keyPath, key, 0600))
	evidence := map[string]any{"request_sha256": digest, "lab_id": "isolated", "role": "execution", "old_boot": req.Grant.TargetBootID, "new_boot": "22222222-2222-4222-8222-222222222222", "controller_boot": "33333333-3333-4333-8333-333333333333", "controller_boot_unchanged": true, "control_boot_unchanged": true, "old_qemu_pid": 42, "old_qemu_start": "123", "qemu_exited": true, "disk_device_inode": []uint64{1, 2}, "provider_scope": "local-only", "operation_result": "unknown", "status": "fenced", "observed_at": time.Now().UTC(), "provider": map[string]any{"external_storage": false, "docker_exec_inflight": true, "libvirt_endpoint": "qemu:///system"}, "post_boot_provider_state": []string{"false", "shut off"}}
	execute := func() (string, error) {
		c := newHostFenceCommand()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs([]string{"attest", admission, report, "--key", keyPath})
		err := c.Execute()
		return out.String(), err
	}
	save(report, evidence)
	output, err := execute()
	require.NoError(t, err)
	var receipt controlplane.HostFenceReceipt
	require.NoError(t, json.Unmarshal([]byte(output), &receipt))
	require.NoError(t, controlplane.VerifyHostFenceReceipt(req, receipt))
	for field, bad := range map[string]any{"request_sha256": strings.Repeat("c", 64), "lab_id": "historical", "old_boot": "44444444-4444-4444-8444-444444444444", "controller_boot_unchanged": false, "operation_result": "success", "status": "intent", "provider": map[string]any{"external_storage": true}} {
		t.Run(field, func(t *testing.T) {
			old := evidence[field]
			evidence[field] = bad
			save(report, evidence)
			_, err := execute()
			require.Error(t, err)
			evidence[field] = old
		})
	}
	save(report, evidence)
	require.NoError(t, os.Chmod(keyPath, 0644))
	_, err = execute()
	require.Error(t, err)
}
