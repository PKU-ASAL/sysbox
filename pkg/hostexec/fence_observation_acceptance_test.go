package hostexec

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

// Runs only inside an explicitly scoped disposable acceptance VM. Fixtures are
// read-only canonical snapshots; observations use the actual local providers.
func TestFenceObservationAcceptance(t *testing.T) {
	path := os.Getenv("SYSBOX_FENCE_OBSERVER_INPUT")
	if path == "" {
		t.Skip("disposable VM observer fixture required")
	}
	var input struct {
		State   *state.State
		Serial  int64
		Actions []runtime.ActionRecord
		Proof   hostsupervisor.RunRecoveryRequest
		APIURL  string
		Key     []byte
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &input))
	input.State.Meta.Serial = input.Serial
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	require.NoError(t, err)
	d, err := docker.New()
	require.NoError(t, err)
	defer d.Close()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "acceptance", Node: d, NodeState: d}))
	l := libvirt.New("/var/tmp/sysbox-delete-closure")
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "libvirt", Version: "acceptance", Node: l, NodeState: l}))
	repo := &recoveryTestRepo{st: input.State, actions: input.Actions}
	host := RunExecutor{Bridge: recoveryTestBridge{repo: repo}, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent", HostBootID: strings.TrimSpace(string(boot)), HostKey: input.Key}, Authority: hostsupervisor.AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: input.APIURL}}}
	report, err := host.Recover(context.Background(), input.Proof)
	require.NoError(t, err)
	require.NotNil(t, report.HostObservation)
	for _, r := range report.Resources {
		if r.ResetSource != nil {
			require.Equal(t, "unknown", r.Status, "resource absence is not composite reset success")
			if r.ResetReplacement != nil {
				require.Equal(t, "present", r.ResetReplacement.Status)
				require.Equal(t, "local-only", r.ResetReplacement.ProviderScope)
				require.Greater(t, r.ResetReplacement.Generation, r.ResetSource.Generation)
			}
			r = *r.ResetSource
		}
		require.Equal(t, "absent", r.Status)
		require.Equal(t, "local-only", r.ProviderScope)
		require.True(t, r.DeleteResidueAbsent)
	}
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("SYSBOX_FENCE_OBSERVER_OUTPUT"), encoded, 0600))
}
