package agentexec

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRemoteFileRequiresSupervisorWithoutFallback(t *testing.T) {
	_, err := runGrantedFileInstall(context.Background(), Options{}, controlplane.AgentCommand{})
	require.ErrorContains(t, err, "host supervisor socket")
}

func TestRemoteFileRunnerNeverReadsStateOrClaimsCompletion(t *testing.T) {
	opts := Options{ReportGuestFileStartFunc: func(context.Context, string) error { return nil }}
	runner := newCommandRunner(opts)
	cmd := controlplane.AgentCommand{ID: "command", Type: "guest_file_put", FilePut: &controlplane.GuestFilePut{ID: "file", Topology: "lab", Node: "web"}}
	var last controlplane.AgentCommandEvent
	runner.Execute(context.Background(), &cmd, func(event controlplane.AgentCommandEvent) { last = event })
	require.Equal(t, "failed", last.Status)
	require.Contains(t, last.Error, "host supervisor socket")
}
