package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func executionDockerAPI(t *testing.T, stdout, stderr string, inspect container.ExecInspect) *Substrate {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/node/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "node", "Config": map[string]any{"Labels": map[string]string{"sysbox.generation": "4", "sysbox.managed": "true", "sysbox.owner_uid": "owner", "sysbox.resource_uid": "resource", "sysbox.topology_uid": "topology"}}})
		case strings.HasSuffix(r.URL.Path, "/containers/node/exec"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "exec-opaque"})
		case strings.HasSuffix(r.URL.Path, "/exec/exec-opaque/start"):
			var options container.ExecStartOptions
			if json.NewDecoder(r.Body).Decode(&options) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			if options.Detach {
				w.WriteHeader(http.StatusOK)
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			_, _ = io.WriteString(stdcopy.NewStdWriter(conn, stdcopy.Stdout), stdout)
			_, _ = io.WriteString(stdcopy.NewStdWriter(conn, stdcopy.Stderr), stderr)
		case strings.HasSuffix(r.URL.Path, "/exec/exec-opaque/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(inspect)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(strings.Replace(server.URL, "http://", "tcp://", 1)), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return &Substrate{cli: cli}
}

func TestDockerConnectionAndLegacyExecShareBoundedResults(t *testing.T) {
	sub := executionDockerAPI(t, "abcdef", "12345", container.ExecInspect{ExitCode: 7})
	handle := substrate.NodeHandle{ID: "node", Generation: 4}
	conn, err := sub.Connection(handle, nil)
	require.NoError(t, err)
	var stdout bytes.Buffer
	req := substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone, OutputLimitBytes: 3}
	result, err := conn.Exec(context.Background(), req, &stdout, nil)
	require.NoError(t, err)
	require.Equal(t, "abc", stdout.String())
	require.Equal(t, "abc", result.Stdout)
	require.Equal(t, "123", result.Stderr)
	require.True(t, result.StdoutTruncated)
	require.True(t, result.StderrTruncated)
	require.True(t, result.ExitCodeKnown)
	require.Equal(t, 7, result.ExitCode)
	legacy, err := sub.ExecInNode(context.Background(), handle, req)
	require.NoError(t, err)
	require.Equal(t, result, legacy)
}

func TestDockerConnectionPreservesOutputWhenInspectSaysStillRunning(t *testing.T) {
	sub := executionDockerAPI(t, "partial", "", container.ExecInspect{Running: true})
	conn, err := sub.Connection(substrate.NodeHandle{ID: "node", Generation: 4}, nil)
	require.NoError(t, err)
	result, err := conn.Exec(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone}, nil, nil)
	require.Error(t, err)
	require.Equal(t, "partial", result.Stdout)
	require.False(t, result.ExitCodeKnown)
	require.Equal(t, substrate.RemoteTerminationUnknown, result.RemoteTermination)
}

func TestDockerStartUsesExecutionIDAndBoundNodeGeneration(t *testing.T) {
	sub := executionDockerAPI(t, "", "", container.ExecInspect{Running: true, Pid: 42})
	conn, err := sub.Connection(substrate.NodeHandle{ID: "node", Generation: 4}, nil)
	require.NoError(t, err)
	starter, ok := conn.(substrate.ConnectionStarter)
	require.True(t, ok, "Docker must expose optional Start")
	handle, err := starter.Start(context.Background(), substrate.ExecRequest{Program: "test", Shell: substrate.ShellNone})
	require.NoError(t, err)
	require.Equal(t, "exec-opaque", handle.ID)
	require.Equal(t, "node", handle.NodeID)
	require.EqualValues(t, 4, handle.Generation)
	require.Equal(t, 42, handle.PID)
}
