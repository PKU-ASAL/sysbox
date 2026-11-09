package commands

import (
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetResumeRejectsChangedTarget(t *testing.T) {
	cmd := newLifecycleCommand("reset")
	cmd.SetArgs([]string{"--resume", "old-run", "--target", "sysbox_node.web"})
	require.ErrorContains(t, cmd.Execute(), "--resume retains")
}

func TestResetCLITransmitsInputsWithoutPublishingThem(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			inputs := filepath.Join(t.TempDir(), "inputs.json")
			require.NoError(t, os.WriteFile(inputs, []byte(`{"password":"private-canary"}`), 0600))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "parent", Topology: "lab", Op: "reset"})
					return
				}
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "once", r.Header.Get("Idempotency-Key"))
				var body struct {
					Inputs map[string]string `json:"inputs"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "private-canary", body.Inputs["password"])
				w.WriteHeader(202)
				_, _ = w.Write([]byte(`{"run_id":"child"}`))
			}))
			defer server.Close()
			cmd := newLifecycleCommand("reset")
			args := []string{"--api", server.URL, "--topology", "lab", "--key", "once", "--inputs", inputs, "--auto-approve", "--detach"}
			if resume {
				args = append(args, "--resume", "parent")
			} else {
				args = append(args, "--revision", "sha256:"+strings.Repeat("a", 64))
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
		})
	}
}
