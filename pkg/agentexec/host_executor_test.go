package agentexec

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
)

type hostClaimBridge struct {
	Bridge
	run          *controlplane.Run
	materialized bool
}

func (b *hostClaimBridge) ParentRun(context.Context, string) (*controlplane.Run, error) {
	return b.run, nil
}
func (b *hostClaimBridge) MaterializeRunRevision(context.Context, string, string) (string, error) {
	b.materialized = true
	return "", nil
}

func TestHostRunExecutorRejectsChangedCanonicalClaimBeforeMaterialization(t *testing.T) {
	for _, field := range []string{"agent", "revision", "status", "lease"} {
		t.Run(field, func(t *testing.T) {
			run := &controlplane.Run{ID: "run", Topology: "lab", Revision: "revision", Op: "apply", AgentID: "agent", Status: controlplane.RunRunning, LeaseOwner: "owner", LeaseUntil: time.Now().Add(time.Minute)}
			switch field {
			case "agent":
				run.AgentID = "other"
			case "revision":
				run.Revision = "other"
			case "status":
				run.Status = controlplane.RunDone
			case "lease":
				run.LeaseUntil = time.Now().Add(-time.Minute)
			}
			bridge := &hostClaimBridge{run: run}
			executor := HostRunExecutor{Bridge: bridge, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent"}}
			_, err := executor.Execute(context.Background(), hostsupervisor.RunRequest{RunID: "run", Topology: "lab", Revision: "revision", Action: "apply"})
			require.ErrorContains(t, err, "canonical claim changed")
			require.False(t, bridge.materialized)
		})
	}
}
