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

	"github.com/oslab/sysbox/pkg/substrate"
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
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		if _, err := io.WriteString(conn, "OK 1024\n"); err != nil {
			return
		}
		var request vsockrpc.Request
		if json.NewDecoder(reader).Decode(&request) != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Stdout: []byte("321\n")})
		_ = json.NewEncoder(conn).Encode(vsockrpc.Frame{Done: true})
	}()
	sub := &Substrate{}
	conn, err := sub.Connection(substrate.NodeHandle{ID: "vm", Generation: 7, Provider: &HandleState{VsockUDS: path}}, nil)
	require.NoError(t, err)
	starter, ok := conn.(substrate.ConnectionStarter)
	require.True(t, ok)
	handle, err := starter.Start(context.Background(), substrate.ExecRequest{Program: "sleep", Args: []string{"600"}, Shell: substrate.ShellNone})
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
