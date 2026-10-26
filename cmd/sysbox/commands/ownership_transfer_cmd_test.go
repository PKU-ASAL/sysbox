package commands

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipTransferCLIUsesPublicProtocol(t *testing.T) {
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "move", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "create", OwnerUID: "source", TopologyUID: "source", ResourceUID: "old", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "move", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	for _, operation := range []string{"prepare", "prepared", "submit", "abort"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				suffix := "/" + operation
				if operation == "submit" {
					suffix = ""
				}
				require.Equal(t, "/v1/topologies/target/ownership-transfers"+suffix, r.URL.Path)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "Bearer credential", r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("X-Sysbox-Roles"))
				var got controlplane.OwnershipTransferPlan
				if operation == "prepared" {
					var proof controlplane.OwnershipPreparation
					require.NoError(t, json.NewDecoder(r.Body).Decode(&proof))
					got = proof.Plan
				} else {
					require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
				}
				require.Equal(t, plan, got)
				if operation == "prepared" {
					w.WriteHeader(200)
				} else {
					w.WriteHeader(202)
				}
				_, _ = w.Write([]byte("{\"status\":\"pending\"}"))
			}))
			defer server.Close()
			var body any = plan
			if operation == "prepared" {
				_, key, err := ed25519.GenerateKey(rand.Reader)
				require.NoError(t, err)
				proof, err := controlplane.SignOwnershipPreparation(key, controlplane.OwnershipPreparation{Plan: plan, SourceEpoch: 1, HostBootID: "boot", ObservedAt: time.Now().UTC()})
				require.NoError(t, err)
				body = proof
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "plan.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			command := newOwnershipTransferCommand()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetArgs([]string{operation, path, "--api", server.URL, "--token", "credential"})
			require.NoError(t, command.Execute())
			require.Equal(t, 1, calls)
			require.Contains(t, out.String(), "pending")
			invalid := newOwnershipTransferCommand()
			invalid.SetArgs([]string{operation, path, "--api", server.URL + "?token=secret"})
			require.Error(t, invalid.Execute())
			require.Equal(t, 1, calls)
		})
	}
}

func TestOwnershipTransferCLIRejectsRedirectAndInvalidInput(t *testing.T) {
	calls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(202) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "move", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "create", OwnerUID: "source", TopologyUID: "source", ResourceUID: "old", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "move", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	command := newOwnershipTransferCommand()
	command.SetArgs([]string{"submit", path, "--api", redirect.URL, "--token", "secret"})
	require.ErrorContains(t, command.Execute(), "HTTP 307")
	require.Zero(t, calls)
	for _, input := range []string{"{}", string(raw) + " {}", strings.Replace(string(raw), `"target_topology":"target"`, `"target_topology":"../escape"`, 1)} {
		require.NoError(t, os.WriteFile(path, []byte(input), 0600))
		command = newOwnershipTransferCommand()
		command.SetArgs([]string{"submit", path, "--api", destination.URL})
		require.Error(t, command.Execute())
		require.Zero(t, calls)
	}
}
