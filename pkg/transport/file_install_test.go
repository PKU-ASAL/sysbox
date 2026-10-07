package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"testing"
)

func TestVsockFileInstallLostResponsePreservesFullProof(t *testing.T) {
	received := make(chan guesttask.Call, 1)
	conn := transferPeer(t, func(_ net.Conn, r *bufio.Reader, req vsockrpc.Request) {
		raw, err := io.ReadAll(io.LimitReader(r, req.Size))
		if err != nil {
			return
		}
		var call guesttask.Call
		if json.Unmarshal(raw, &call) == nil {
			received <- call
		}
	})
	conn.BindExecutionIdentity("node", 2)
	data := []byte("payload")
	sum := sha256.Sum256(data)
	req := substrate.FileInstallRequest{ID: "file", Path: "/tmp/installed", Mode: 0600, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	digest, err := req.Digest()
	require.NoError(t, err)
	grant := controlplane.SignedExecutionGrant{BootID: "boot", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 2}, Operation: "guest_file_install", Digest: digest, Epoch: 3}}
	h, _, err := conn.InstallFile(WithTaskGrant(context.Background(), req.ID, grant), req, bytes.NewReader(data))
	require.Error(t, err)
	require.Equal(t, substrate.FileInstallHandle{ID: "file", Digest: digest, BootID: "boot", NodeID: "node", TopologyUID: "topology", ResourceUID: "resource", Generation: 2, Epoch: 3}, h)
	call := <-received
	require.Equal(t, "file-install", call.Operation)
	require.Equal(t, data, call.FileData)
	require.Equal(t, req, *call.FileRequest)
}

func TestFileObserveRejectsWrongGuestProof(t *testing.T) {
	expected := substrate.FileInstallHandle{ID: "file", Digest: "digest", BootID: "boot", NodeID: "node", TopologyUID: "topology", ResourceUID: "resource", Generation: 2, Epoch: 3}
	conn := transferPeer(t, func(c net.Conn, r *bufio.Reader, req vsockrpc.Request) {
		io.CopyN(io.Discard, r, req.Size)
		wrong := expected
		wrong.NodeID = ""
		wrong.Epoch++
		raw, _ := json.Marshal(guesttask.Response{Version: 1, FileRecord: &guesttask.FileInstallRecord{Handle: wrong}})
		json.NewEncoder(c).Encode(vsockrpc.Frame{Done: true, Task: raw})
	})
	conn.BindExecutionIdentity("node", 2)
	_, err := conn.ObserveFileInstall(context.Background(), expected)
	require.ErrorIs(t, err, guesttask.ErrConflict)
}

type fileProtocolConnection struct{ seen guesttask.Call }

func (c *fileProtocolConnection) Exec(_ context.Context, req substrate.ExecRequest, _ io.Writer, _ io.Writer) (substrate.ExecResult, error) {
	raw, err := io.ReadAll(req.Stdin)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	if err = json.Unmarshal(raw, &c.seen); err != nil {
		return substrate.ExecResult{}, err
	}
	encoded, _ := json.Marshal(guesttask.Response{Version: 1, BootID: "fileboot"})
	return substrate.ExecResult{ExitCodeKnown: true, Stdout: string(encoded)}, nil
}
func (*fileProtocolConnection) CopyTo(context.Context, string, string, uint32) error { return nil }
func (*fileProtocolConnection) Probe(context.Context) error                          { return nil }
func TestHelperFileInfoUsesFixedProtocol(t *testing.T) {
	conn := &fileProtocolConnection{}
	client := NewTaskClient(conn, "node", 1)
	boot, err := client.GuestFileBootID(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fileboot", boot)
	require.Equal(t, "file-info", conn.seen.Operation)
}
