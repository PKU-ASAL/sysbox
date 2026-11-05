package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

// A background provisioner must work on any image, so docker implements the
// starter against the daemon's detached exec instead of a guest-side helper.
func TestDockerBackgroundExecutionUsesTheDaemon(t *testing.T) {
	running := true
	execCreates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/container-id/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "container-id",
				"Config": map[string]any{"Labels": map[string]string{
					"sysbox.managed":      "true",
					"sysbox.owner_uid":    "owner",
					"sysbox.resource_uid": "resource",
					"sysbox.topology_uid": "topology",
					"sysbox.generation":   "1",
				}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/container-id/exec"):
			execCreates++
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "exec-" + string(rune('0'+execCreates))})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/exec-2/start"):
			running = false
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/exec-1/start"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/exec-1/json"):
			exitCode := 0
			if !running {
				exitCode = 137
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Running": running, "Pid": 4242, "ExitCode": exitCode})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/exec-2/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Running": false, "Pid": 4243, "ExitCode": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	sub := &Substrate{cli: cli}
	conn := &dockerConn{sub: sub, handle: substrate.NodeHandle{ID: "container-id", Generation: 1}}

	handle, err := conn.Start(context.Background(), substrate.ExecRequest{Program: "sleep", Args: []string{"30"}, Shell: substrate.ShellNone})
	require.NoError(t, err)
	require.Equal(t, "exec-1", handle.ID)
	require.Equal(t, 4242, handle.PID)

	observed, err := conn.ObserveExecution(context.Background(), handle)
	require.NoError(t, err)
	require.Equal(t, substrate.ExecStatusRunning, observed.Status)
	require.False(t, observed.ExitCodeKnown, "a running exec has no exit verdict yet")

	// Cancellation signals the process on the docker host, so it is deliberately
	// refused when the PID cannot be proven local (here it is a fake PID).
	termination, err := conn.CancelExecution(context.Background(), handle)
	require.Error(t, err)
	require.Equal(t, substrate.RemoteTerminationUnknown, termination)
}
