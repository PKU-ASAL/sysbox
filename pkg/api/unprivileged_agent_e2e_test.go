//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestUnprivilegedAgentDockerE2E(t *testing.T) {
	if os.Getenv("SYSBOX_CLI_BINARY") == "" || os.Geteuid() != 0 {
		t.Skip("requires current CLI and isolated root Docker test host")
	}
	testRemoteDockerCompletionLossRecovery(t, false, false)
}

func workerTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sysbox-worker-test-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0755))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	return root
}

func independentWorker(t *testing.T, root string, opts *agentexec.Options, issuer, host ed25519.PublicKey, protected ...string) func(context.Context, chan<- error) {
	t.Helper()
	data := filepath.Join(root, "worker")
	require.NoError(t, os.Mkdir(data, 0700))
	require.NoError(t, os.Chown(data, 32000, 32000))
	opts.ExecutionSessionFile = filepath.Join(data, "session.json")
	identity := filepath.Join(data, "identity.json")
	require.NoError(t, agent.SaveIdentity(identity, &agent.Identity{ID: opts.ID, APIURL: opts.APIURL, Secret: opts.Secret}))
	require.NoError(t, os.Chown(identity, 32000, 32000))
	opts.ExecutionGrantPublicKeyFile, opts.HostPublicKeyFile = filepath.Join(root, "issuer.pub"), filepath.Join(root, "host.pub")
	require.NoError(t, os.WriteFile(opts.ExecutionGrantPublicKeyFile, issuer, 0444))
	require.NoError(t, os.WriteFile(opts.HostPublicKeyFile, host, 0444))
	cfg := config.DefaultServiceConfig()
	cfg.Paths.Home, cfg.Paths.RunsDir, cfg.Paths.WorkspacesDir, cfg.Paths.Cache = data, data, filepath.Join(data, "workspaces"), filepath.Join(data, "cache")
	cfg.Agent.HostSupervisorSocket, cfg.Agent.ExecutionGrantPublicKeyFile, cfg.Agent.HostPublicKeyFile, cfg.Agent.ExecutionSessionFile = opts.HostSupervisorSocket, opts.ExecutionGrantPublicKeyFile, opts.HostPublicKeyFile, opts.ExecutionSessionFile
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	configPath := filepath.Join(root, "worker.json")
	require.NoError(t, os.WriteFile(configPath, raw, 0444))
	args := []string{"--reuid=32000", "--regid=32000", "--clear-groups", "--no-new-privs", "--bounding-set=-all"}
	privateKey := filepath.Join(root, "host.key")
	require.NoError(t, os.WriteFile(privateKey, []byte("root-only signing key sentinel"), 0600))
	for _, path := range append(protected, privateKey) {
		probe := exec.Command("setpriv", append(append([]string{}, args...), "cat", path)...)
		output, err := probe.CombinedOutput()
		require.Error(t, err, "worker must not read %s", path)
		require.Contains(t, string(output), "Permission denied", string(output))
	}
	// Connecting to the real provider socket under the same UID must fail.
	probe := exec.Command("setpriv", append(append([]string{}, args...), "docker", "version")...)
	output, err := probe.CombinedOutput()
	require.Error(t, err, string(output))
	require.Contains(t, strings.ToLower(string(output)), "permission denied", string(output))
	t.Log("UID 32000 cannot read host key/canonical storage or connect to provider socket")
	return func(ctx context.Context, done chan<- error) {
		command := exec.CommandContext(ctx, "setpriv", append(append([]string{}, args...), os.Getenv("SYSBOX_CLI_BINARY"), "agent", "start", "--identity", identity, "--config", configPath, "--poll-interval", "100ms")...)
		command.Dir = "/"
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=invalid://worker-must-not-connect"}
		log, err := os.CreateTemp(root, "worker-log-")
		require.NoError(t, err)
		command.Stdout, command.Stderr = log, log
		require.NoError(t, command.Start())
		// Check the live process, in addition to the CLI's own startup checks.
		require.Eventually(t, func() bool {
			status, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", command.Process.Pid))
			return e == nil && strings.Contains(string(status), "Uid:\t32000\t32000") && strings.Contains(string(status), "CapEff:\t0000000000000000") && strings.Contains(string(status), "NoNewPrivs:\t1")
		}, time.Second, 10*time.Millisecond)
		go func() {
			err := command.Wait()
			log.Close()
			output, _ := os.ReadFile(log.Name())
			if ctx.Err() == nil {
				t.Logf("worker exited: %v\n%s", err, output)
			}
			done <- err
		}()
	}
}
