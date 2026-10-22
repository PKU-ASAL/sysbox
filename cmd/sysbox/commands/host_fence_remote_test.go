package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func intentCLIInput(t *testing.T) string {
	t.Helper()
	d := controlplane.IntentDispositionDecision{Version: 1, ID: "decision-1", FenceID: "fence-1", Topology: "lab", RunID: "run-1", RunQueuedAt: time.Now().UTC(), RunRevision: "rev", ExpectedSerial: 2, JournalSHA256: strings.Repeat("a", 64), ObservationSHA256: strings.Repeat("b", 64), Reason: "retain fenced resource", Actions: []controlplane.IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "uid", Generation: 1, ExternalID: "external"}}}
	raw, err := json.Marshal(map[string]any{"decision": d, "observation_command_id": "obs-1"})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "decision.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	return path
}

func TestHostFenceRemoteRecovery(t *testing.T) {
	path := intentCLIInput(t)
	for _, mode := range []string{"recovery-plan", "observe", "reconcile-intents"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/v1/topologies/lab/host-fences/fence-1/"+mode, r.URL.Path)
				require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("X-Sysbox-Roles"))
				if mode == "recovery-plan" {
					require.Equal(t, "GET", r.Method)
					require.Equal(t, "obs-1", r.URL.Query().Get("observation"))
					require.Empty(t, r.Header.Get("Idempotency-Key"))
				} else {
					require.Equal(t, "POST", r.Method)
					if mode == "observe" {
						require.Equal(t, "observe-1", r.Header.Get("Idempotency-Key"))
						w.WriteHeader(202)
					} else {
						require.Equal(t, "decision-1", r.Header.Get("Idempotency-Key"))
						var got struct {
							Decision    controlplane.IntentDispositionDecision `json:"decision"`
							Observation string                                 `json:"observation_command_id"`
						}
						require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
						require.Equal(t, "obs-1", got.Observation)
						require.Equal(t, "decision-1", got.Decision.ID)
					}
				}
				_, _ = w.Write([]byte(`{"status":"recorded"}`))
			}))
			defer server.Close()
			t.Setenv("SYSBOX_API_TOKEN", "secret")
			args := []string{mode, "fence-1", "--topology", "lab", "--api", server.URL}
			switch mode {
			case "recovery-plan":
				args = append(args, "--observation", "obs-1")
			case "observe":
				args = append(args, "--key", "observe-1")
			case "reconcile-intents":
				args = append(args, "--decision", path)
			}
			cmd := newHostFenceCommand()
			cmd.SetArgs(args)
			var out bytes.Buffer
			cmd.SetOut(&out)
			require.NoError(t, cmd.Execute())
			require.Equal(t, 1, calls)
			require.JSONEq(t, `{"status":"recorded"}`, out.String())
		})
	}
}

func TestHostFenceRemoteRejectsUnsafeInputAndRedirect(t *testing.T) {
	path := intentCLIInput(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Redirect(w, r, "/redirected", 307) }))
	defer server.Close()
	base := []string{"reconcile-intents", "fence-1", "--topology", "lab", "--decision", path, "--api", server.URL}
	cmd := newHostFenceCommand()
	cmd.SetArgs(base)
	require.ErrorContains(t, cmd.Execute(), "HTTP 307")
	require.Equal(t, 1, calls)
	for _, args := range [][]string{
		{"recovery-plan", "..", "--topology", "lab", "--api", server.URL},
		{"recovery-plan", "fence-1", "--topology", "lab", "--api", server.URL + "?token=secret"},
		{"reconcile-intents", "other", "--topology", "lab", "--decision", path, "--api", server.URL},
		{"observe", "fence-1", "--topology", "lab", "--api", server.URL},
	} {
		cmd := newHostFenceCommand()
		cmd.SetArgs(args)
		require.Error(t, cmd.Execute())
	}
	require.Equal(t, 1, calls)
}

func TestIntentDecisionRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	finished := make(chan error, 1)
	go func() { finished <- readIntentDecision(path, new(any)) }()
	select {
	case err := <-finished:
		require.ErrorContains(t, err, "regular JSON file")
	case <-time.After(time.Second):
		// Unblock a broken implementation so the regression does not leak a goroutine.
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
		t.Fatal("decision reader blocked opening FIFO")
	}
}
