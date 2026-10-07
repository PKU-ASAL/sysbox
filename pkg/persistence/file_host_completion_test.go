package persistence

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestFileMetadataRequiresNewHostReceiptFormat(t *testing.T) {
	x := controlplane.GuestFileOperation{ID: "file", Version: 1, Status: controlplane.GuestExecutionQueued}
	raw, err := fileOperationCodec.encode(x)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(raw, &record))
	record["record_format"] = 1
	raw, err = json.Marshal(record)
	require.NoError(t, err)
	_, err = fileOperationCodec.decode(raw)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestFileHostCompletionAcceptsConfirmedCommitAfterCancelAndLeaseExpiry(t *testing.T) {
	x := controlplane.GuestFileOperation{ID: "install", Version: 1, AgentID: "agent", CommandID: "command", Topology: "lab", Node: "vm", Status: controlplane.GuestExecutionRunning, CancelRequested: true}
	cmd := controlplane.AgentCommand{ID: "command", AgentID: "agent", Type: "guest_file_put", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, ActionID: x.ID, FilePut: &controlplane.GuestFilePut{ID: x.ID, Topology: x.Topology, Node: x.Node, Path: "/tmp/file", Size: 3, Mode: 0600, SHA256: strings.Repeat("a", 64)}}
	req, err := controlplane.FromFileInstallCommand(cmd)
	require.NoError(t, err)
	digest, err := req.Digest()
	require.NoError(t, err)
	receipt := controlplane.SignedExecutionStopReceipt{InstallID: x.ID, InstallDigest: digest, Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", CommandID: cmd.ID, ActionID: x.ID, Digest: digest, Operation: "guest_file_install"}, FileResult: &substrate.FileInstallResult{Outcome: substrate.FileInstallCommitted, Quiescent: true, Size: 3, Mode: 0600, SHA256: req.SHA256}}
	raw, err := fileOperationCodec.encode(x)
	require.NoError(t, err)
	completed, err := CompleteFileFromHost(raw, &CommandRecord{Command: cmd, Retired: true}, "agent", x.ID, receipt, time.Now())
	require.NoError(t, err)
	got, err := fileOperationCodec.decode(completed)
	require.NoError(t, err)
	require.Equal(t, controlplane.GuestExecutionCompleted, got.Status)
	require.Equal(t, substrate.FileInstallCommitted, got.Result.Outcome)
	again, err := CompleteFileFromHost(completed, &CommandRecord{Command: cmd}, "agent", x.ID, receipt, time.Now())
	require.NoError(t, err)
	require.Equal(t, string(completed), string(again))
	receipt.FileResult.Quiescent = false
	_, err = CompleteFileFromHost(raw, &CommandRecord{Command: cmd}, "agent", x.ID, receipt, time.Now())
	require.Error(t, err)
}
