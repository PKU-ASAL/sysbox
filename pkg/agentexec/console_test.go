package agentexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestConsoleFallbackReportsUnknownExitAsFailure(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "known_success"}[known], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			complete := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					complete <- err
					return
				}
				defer ws.CloseNow()
				complete <- runConsoleFallback(ctx, &fixedGuestConnection{result: substrate.ExecResult{ExitCodeKnown: known}}, substrate.ConsoleRequest{}, ws)
			}))
			defer server.Close()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer ws.CloseNow()
			var frames []consoleFrame
			for {
				_, raw, err := ws.Read(ctx)
				if err != nil {
					break
				}
				var frame consoleFrame
				require.NoError(t, json.Unmarshal(raw, &frame))
				frames = append(frames, frame)
				if frame.Type == "exit" {
					break
				}
			}
			select {
			case err := <-complete:
				if known {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "exit status unknown")
				}
			case <-ctx.Done():
				t.Fatal("console fallback did not finish")
			}
			require.NotEmpty(t, frames, "console must report completion before returning")
			last := frames[len(frames)-1]
			require.Equal(t, "exit", last.Type)
			if known {
				require.Equal(t, 0, last.Code)
			} else {
				require.NotZero(t, last.Code, "unknown exit must never be reported as successful exit zero")
				require.Equal(t, "error", frames[0].Type)
				require.Contains(t, frames[0].Error, "exit status unknown")
			}
		})
	}
}
