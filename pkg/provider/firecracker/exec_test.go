package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
)

func TestLegacyExecUsesNodeConnectionSelection(t *testing.T) {
	sub := &Substrate{}
	handle := substrate.NodeHandle{ID: "vm", Net: substrate.NetInfo{PrimaryIP: "192.0.2.1"}}
	req := substrate.ExecRequest{Program: "true", Shell: substrate.ShellNone, Environment: map[string]string{"BAD KEY": "value"}}
	_, err := sub.ExecInNode(context.Background(), handle, req)
	require.ErrorContains(t, err, "invalid environment key")
}

func TestVsockConnectionStartBindsExecutionToNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vsock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				if _, err := reader.ReadString('\n'); err != nil {
					return
				}
				if _, err := io.WriteString(conn, "OK 1024\n"); err != nil {
					return
				}
				var request vsockrpc.Request
				header, err := reader.ReadBytes('\n')
				if err != nil || json.Unmarshal(header, &request) != nil {
					return
				}
				if request.Op == vsockrpc.OpReady {
					_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true, Ready: true})
					return
				}
				var call guesttask.Call
				if json.NewDecoder(io.LimitReader(reader, request.Size)).Decode(&call) != nil {
					return
				}
				digest, _ := call.Request.Digest()
				raw, _ := json.Marshal(guesttask.Response{Version: 1, Record: &guesttask.Record{ID: call.Request.ID, Digest: digest, PID: 321}})
				_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true, Task: raw})
			}()
		}
	}()
	sub := &Substrate{}
	conn, err := sub.Connection(substrate.NodeHandle{ID: "vm", Generation: 7, Provider: &HandleState{VsockUDS: path}}, nil)
	require.NoError(t, err)
	starter, ok := conn.(substrate.ConnectionStarter)
	require.True(t, ok)
	req := substrate.ExecRequest{Program: "sleep", Args: []string{"600"}, Shell: substrate.ShellNone}
	task, err := transport.BuildTaskRequest(context.Background(), "durable-task", req)
	require.NoError(t, err)
	digest, err := task.Digest()
	require.NoError(t, err)
	ctx := transport.WithTaskGrant(context.Background(), task.ID, controlplane.SignedExecutionGrant{BootID: "boot", Grant: controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{Generation: 7}, Digest: digest, Epoch: 1}})
	handle, err := starter.Start(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "vm", handle.NodeID)
	require.EqualValues(t, 7, handle.Generation)
	require.Equal(t, 321, handle.PID)
	require.NotEmpty(t, handle.ID)
	require.NotEqual(t, "321", handle.ID)
}

func TestSSHConnectionBindsNamespaceAndGeneration(t *testing.T) {
	sub := &Substrate{}
	handle := substrate.NodeHandle{ID: "vm", Generation: 3, Provider: &HandleState{SSHIP: "192.0.2.1", NetnsName: "sandbox"}}
	conn, err := sub.Connection(handle, nil)
	require.NoError(t, err)
	value := reflect.ValueOf(conn).Elem()
	require.Equal(t, "sandbox", value.FieldByName("netns").String())
	require.Equal(t, "vm", value.FieldByName("executionIdentity").FieldByName("nodeID").String())
	require.EqualValues(t, 3, value.FieldByName("executionIdentity").FieldByName("generation").Int())
}
