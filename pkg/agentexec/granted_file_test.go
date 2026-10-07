package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestRemoteFileRequiresSupervisorWithoutFallback(t *testing.T) {
	_, err := runGrantedFileInstall(context.Background(), Options{}, controlplane.AgentCommand{})
	require.ErrorContains(t, err, "host supervisor socket")
}

func TestRemoteFileRunnerNeverReadsStateOrClaimsCompletion(t *testing.T) {
	dir := t.TempDir()
	bridge := &nodeOperationProofBridge{LocalBridge: NewLocalBridge(LocalOptions{Topology: "lab", RunsDir: dir, StatePath: filepath.Join(dir, "state.json")})}
	opts := Options{ReportGuestFileStartFunc: func(context.Context, string) error { return nil }}
	runner := newCommandRunner(NewExecutorWithBridge(bridge), opts)
	cmd := controlplane.AgentCommand{ID: "command", Type: "guest_file_put", FilePut: &controlplane.GuestFilePut{ID: "file", Topology: "lab", Node: "web"}}
	var last controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { last = event })
	require.Zero(t, bridge.stateReads)
	require.Equal(t, "failed", last.Status)
	require.Contains(t, last.Error, "host supervisor socket")
}
