//go:build e2e

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func testServiceConsoleSessions(t *testing.T, ctx context.Context, url, topology, node, containerID string, request func(string, string, any, string) (int, []byte)) {
	for _, scenario := range []string{"normal", "disconnect", "cancel", "expiry"} {
		t.Run(scenario, func(t *testing.T) {
			command := "sleep 90 & child=$!; printf 'child=%s ready\\n' \"$child\"; read answer; printf 'uid=%s answer=%s\\n' \"$(id -u)\" \"$answer\"; head -c 70000 /dev/zero | tr '\\0' x; exit 7"
			seconds := 60
			if scenario == "expiry" {
				seconds = 3
			}
			code, raw := request("POST", "/v1/topologies/"+topology+"/nodes/"+node+"/sessions", controlplane.ConsoleRequest{Cmd: []string{"sh", "-c", command}, TimeoutSeconds: seconds}, "")
			require.Equal(t, 202, code, string(raw))
			var session controlplane.ConsoleSession
			require.NoError(t, json.Unmarshal(raw, &session))
			defer func() {
				if t.Failed() {
					out, _ := exec.Command("docker", "exec", containerID, "sh", "-c", "cat /var/lib/sysbox/guest-tasks/"+session.ID+"/record.json; cat /var/lib/sysbox/guest-tasks/"+session.ID+"/stderr; cat /var/lib/sysbox/guest-tasks/"+session.ID+"/stdout").CombinedOutput()
					t.Logf("guest task diagnostics: %s", out)
				}
			}()
			headers := http.Header{"X-Sysbox-Roles": []string{"admin"}}
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/v1/sessions/"+session.ID+"/attach", &websocket.DialOptions{HTTPHeader: headers})
			require.NoError(t, err)
			defer ws.CloseNow()
			var output strings.Builder
			read := func() string {
				_, raw, err := ws.Read(ctx)
				require.NoError(t, err)
				var frame struct {
					Type, Data, Error string
					Code              int
				}
				require.NoError(t, json.Unmarshal(raw, &frame))
				if frame.Type == "stdout" {
					data, err := base64.StdEncoding.DecodeString(frame.Data)
					require.NoError(t, err)
					output.Write(data)
				}
				require.NotEqual(t, "error", frame.Type, frame.Error)
				if frame.Type == "exit" {
					require.Equal(t, 7, frame.Code)
				}
				return frame.Type
			}
			for !strings.Contains(output.String(), "ready") {
				read()
			}
			if scenario == "normal" {
				raw, _ := json.Marshal(map[string]any{"type": "resize", "cols": 100, "rows": 40})
				require.NoError(t, ws.Write(ctx, websocket.MessageText, raw))
				raw, _ = json.Marshal(map[string]any{"type": "stdin", "data": base64.StdEncoding.EncodeToString([]byte("console-test\n"))})
				require.NoError(t, ws.Write(ctx, websocket.MessageText, raw))
				for read() != "exit" {
				}
				require.Contains(t, output.String(), "uid=65534 answer=console-test")
				require.GreaterOrEqual(t, len(output.String()), 70000)
			} else if scenario == "disconnect" {
				require.NoError(t, ws.CloseNow())
			} else if scenario == "cancel" {
				code, raw = request("POST", "/v1/sessions/"+session.ID+"/cancel", nil, "")
				require.Equal(t, 200, code, string(raw))
			}
			var final controlplane.ConsoleSession
			require.Eventually(t, func() bool {
				code, raw = request("GET", "/v1/sessions/"+session.ID, nil, "")
				if code != 200 {
					return false
				}
				err = json.Unmarshal(raw, &final)
				return err == nil && final.TerminationConfirmed
			}, 15*time.Second, 100*time.Millisecond)
			require.NotEmpty(t, final.HostCompletion)
			if scenario == "normal" {
				require.NotNil(t, final.ExitCode)
				require.Equal(t, 7, *final.ExitCode)
			} else {
				require.Nil(t, final.ExitCode)
			}
			out, err := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", "cat /sys/fs/cgroup/sysbox-tasks/sysbox-task-"+session.ID+"/cgroup.events").CombinedOutput()
			require.NoError(t, err, string(out))
			require.Contains(t, string(out), "populated 0")
			t.Logf("%s: host receipt persisted, cgroup empty", scenario)
		})
	}
}
