package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"testing"
)

func TestInstalledFileInspectionBindsGuestHandle(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "foreign"}[wrong], func(t *testing.T) {
			h := substrate.FileInstallHandle{ID: "file", Digest: "digest", BootID: "boot", NodeID: "node", TopologyUID: "owner", ResourceUID: "resource", Generation: 2, Epoch: 3}
			conn := transferPeer(t, func(c net.Conn, r *bufio.Reader, req vsockrpc.Request) {
				raw, err := io.ReadAll(io.LimitReader(r, req.Size))
				require.NoError(t, err)
				var call guesttask.Call
				require.NoError(t, json.Unmarshal(raw, &call))
				require.Equal(t, "file-inspect", call.Operation)
				proof := *call.FileHandle
				if wrong {
					proof.Epoch++
				}
				raw, err = json.Marshal(guesttask.Response{Version: guesttask.Version, FileHandle: &proof, FileInspection: &substrate.InstalledFileInspection{Status: "absent"}})
				require.NoError(t, err)
				_ = json.NewEncoder(c).Encode(vsockrpc.Frame{Done: true, Task: raw})
			})
			conn.BindExecutionIdentity("node", 2)
			result, err := conn.InspectInstalledFile(context.Background(), h)
			if wrong {
				require.ErrorIs(t, err, guesttask.ErrConflict)
			} else {
				require.NoError(t, err)
				require.Equal(t, "absent", result.Status)
			}
		})
	}
}
