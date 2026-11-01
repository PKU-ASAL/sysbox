package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCLIUsesServiceWithoutLocalState(t *testing.T) {
	for _, operation := range []string{"apply", "reset", "destroy"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "field.sysbox.hcl")
			require.NoError(t, os.WriteFile(file, []byte(`resource "sysbox_node" "web" { substrate = "docker" }`), 0600))
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				require.Equal(t, "Bearer credential", r.Header.Get("Authorization"))
				switch r.URL.Path {
				case "/v1/revisions":
					var body struct {
						Files map[string]string `json:"files"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Len(t, body.Files, 1)
					files := map[string][]byte{}
					for name, raw := range body.Files {
						files[name] = []byte(raw)
					}
					w.WriteHeader(201)
					_ = json.NewEncoder(w).Encode(map[string]string{"revision": controlplane.ComputeRevisionDigest(files)})
				case "/v1/topologies/lab/" + operation:
					require.Equal(t, "cli-key", r.Header.Get("Idempotency-Key"))
					if operation != "destroy" {
						var body map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						require.Equal(t, "sysbox_node.web", body["target"])
						require.True(t, strings.HasPrefix(body["revision"].(string), "sha256:"))
					}
					w.WriteHeader(202)
					_ = json.NewEncoder(w).Encode(map[string]string{"run_id": "run-one"})
				case "/v1/runs/run-one":
					_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "run-one", Topology: "lab", Op: operation, Status: controlplane.RunDone})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			command := newLifecycleCommand(operation)
			args := []string{"--api", server.URL, "--token", "credential", "--topology", "lab", "--key", "cli-key", "--auto-approve"}
			if operation != "destroy" {
				args = append(args, "--file", file, "--target", "sysbox_node.web")
			}
			command.SetArgs(args)
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			require.NoError(t, command.Execute())
			require.Contains(t, output.String(), "run-one")
			require.Contains(t, requests, "GET /v1/runs/run-one")
			_, err := os.Stat(filepath.Join(dir, ".sysbox"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestCLIUncertainSubmitRetainsKeyAndNeverReadsLocalState(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	defer server.Close()
	cmd := newLifecycleCommand("destroy")
	cmd.SetArgs([]string{"--api", server.URL, "--topology", "lab", "--key", "stable-key", "--auto-approve"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, output.String(), "stable-key")
	require.NotEmpty(t, keys)
	for _, key := range keys {
		require.Equal(t, "stable-key", key)
	}
}

func TestCLIWaitCancellationDoesNotSubmitCancelOrAnotherRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/v1/runs/active", r.URL.Path)
		_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "active", Status: controlplane.RunRunning})
		cancel()
	}))
	defer server.Close()
	cmd := newRunCommand()
	cmd.SetArgs([]string{"wait", "active", "--api", server.URL})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.Error(t, cmd.ExecuteContext(ctx))
	require.Contains(t, output.String(), "active")
}

func TestCLIRevisionIncludesOnlyExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.hcl"), []byte("# root"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("never-upload"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "scripts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "init.sh"), []byte("echo ready"), 0600))
	files, err := readRevisionFiles(filepath.Join(dir, "main.hcl"), []string{"scripts"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"field.sysbox.hcl": "# root", "scripts/init.sh": "echo ready"}, files)
	require.NoError(t, os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "scripts", "link")))
	_, err = readRevisionFiles(filepath.Join(dir, "main.hcl"), []string{"scripts"})
	require.Error(t, err)
	_, err = readRevisionFiles(filepath.Join(dir, "main.hcl"), []string{"../outside"})
	require.Error(t, err)
}

func TestCLIRejectsRedirectAndInvalidEndpoint(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("credential followed redirect") }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer server.Close()
	for _, endpoint := range []string{server.URL, server.URL + "?token=private"} {
		cmd := newLifecycleCommand("destroy")
		cmd.SetArgs([]string{"--api", endpoint, "--token", "credential", "--topology", "lab", "--auto-approve"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := cmd.ExecuteContext(ctx)
		cancel()
		require.Error(t, err)
	}
}

func TestLifecycleCLIRejectsLocalStateFlags(t *testing.T) {
	cmd := newLifecycleCommand("destroy")
	cmd.Flags().String("state", "", "legacy flag inherited from root")
	cmd.SetArgs([]string{"--state", "/must-not-open", "--auto-approve"})
	require.ErrorContains(t, cmd.Execute(), "configure storage on the service")
}

func TestCLIPlanUsesRevisionAndServiceState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/topologies/lab/plan", r.URL.Path)
		require.Equal(t, "POST", r.Method)
		_ = json.NewEncoder(w).Encode(map[string]any{"summary": "No changes.", "actions": []any{}})
	}))
	defer server.Close()
	cmd := newLifecycleCommand("plan")
	cmd.SetArgs([]string{"--api", server.URL, "--topology", "lab", "--revision", "sha256:" + strings.Repeat("a", 64)})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "No changes.")
}

func TestServiceCLICommandsDoNotInitializeProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "run", Status: controlplane.RunDone})
	}))
	defer server.Close()
	rootCmd.SetArgs([]string{"run", "show", "run", "--api", server.URL})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	require.NoError(t, ExecuteContextWithProviders(context.Background(), func(string) error { t.Error("client initialized providers"); return nil }))
}

func TestCLIInputsCannotBeIncludedInRevision(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "config"), 0700))
	file := filepath.Join(dir, "main.hcl")
	input := filepath.Join(dir, "config", "inputs.json")
	require.NoError(t, os.WriteFile(file, []byte("# project"), 0600))
	require.NoError(t, os.WriteFile(input, []byte(`{"flag":"secret-canary"}`), 0600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inputs must be rejected before any upload")
		w.WriteHeader(500)
	}))
	defer server.Close()
	cmd := newLifecycleCommand("apply")
	cmd.SetArgs([]string{"--api", server.URL, "--file", file, "--include", "config", "--inputs", input, "--auto-approve"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.ErrorContains(t, cmd.Execute(), "inputs file")
	require.NoError(t, os.Link(input, filepath.Join(dir, "alias.json")))
	_, err := readRevisionFiles(file, []string{"alias.json"}, input)
	require.ErrorContains(t, err, "inputs file")
}

func TestCLITargetRejectsStructuredScope(t *testing.T) {
	for _, target := range []string{"sysbox_node.web[0]", "module.child.sysbox_node.web"} {
		cmd := newLifecycleCommand("apply")
		cmd.SetArgs([]string{"--target", target, "--revision", "sha256:" + strings.Repeat("a", 64), "--auto-approve"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		require.ErrorContains(t, cmd.Execute(), "unindexed root resource")
	}
}

func TestCLIWaitReportsMissingTransientInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"id":"waiting","status":"assigned","inputs_required":true}`)
	}))
	defer server.Close()
	cmd := newRunCommand()
	cmd.SetArgs([]string{"wait", "waiting", "--api", server.URL})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, cmd.ExecuteContext(ctx), "original key and inputs")
}

func TestHostSupervisorRejectsUntrustedConfigBeforeProviderInitialization(t *testing.T) {
	oldConfig, oldIdentity := supervisorConfigFile, supervisorIdentityFile
	t.Cleanup(func() { supervisorConfigFile, supervisorIdentityFile = oldConfig, oldIdentity; rootCmd.SetArgs(nil) })
	rootCmd.SetArgs([]string{"host-supervisor", "--config", "/nonexistent-sysbox-host/config.yaml"})
	called := false
	err := ExecuteContextWithProviders(context.Background(), func(string) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
}
