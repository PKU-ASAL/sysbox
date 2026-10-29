package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestPowerCLIUsesServiceAndChecksResultIdentity(t *testing.T) {
	for _, op := range []string{"pause", "resume"} {
		for _, mismatch := range []bool{false, true} {
			t.Run(op+"_"+map[bool]string{true: "mismatch", false: "valid"}[mismatch], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					result := controlplane.NodeOperation{ID: "operation", Topology: "lab", Node: "web", Operation: op, Status: controlplane.NodeOperationStatusDone}
					if r.Method == "POST" {
						require.Equal(t, "/v1/topologies/lab/nodes/web/"+op, r.URL.Path)
						require.Equal(t, "stable-key", r.Header.Get("Idempotency-Key"))
						result.Status = controlplane.NodeOperationStatusQueued
						w.WriteHeader(202)
					} else {
						require.Equal(t, "/v1/node-operations/operation", r.URL.Path)
						if mismatch {
							result.Node = "other"
						}
					}
					_ = json.NewEncoder(w).Encode(result)
				}))
				defer server.Close()
				cmd := newPowerCommand(op)
				cmd.SetArgs([]string{"sysbox_node.web", "--api", server.URL, "--topology", "lab", "--key", "stable-key"})
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				err := cmd.Execute()
				if mismatch {
					require.ErrorContains(t, err, "identity")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, 2, calls)
				require.Contains(t, output.String(), "operation")
			})
		}
	}
}

func TestPowerCLIRejectsLocalStateAndStructuredTarget(t *testing.T) {
	for _, args := range [][]string{{"sysbox_node.web", "--state", "/not-opened"}, {"module.app.sysbox_node.web"}, {"sysbox_node.web[0]"}, {"sysbox_network.lab"}} {
		cmd := newPowerCommand("pause")
		cmd.Flags().String("state", "", "legacy inherited flag")
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		require.Error(t, cmd.Execute())
	}
}
