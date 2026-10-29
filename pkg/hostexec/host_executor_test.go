package hostexec

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

type hostClaimBridge struct {
	Bridge
	run          *controlplane.Run
	materialized bool
}

func TestHostRunSSHCallbackRequiresParentBeforeAuthorityOrDispatch(t *testing.T) {
	executor := &RunExecutor{}
	identity := substrate.OperationIdentity{ActionID: "ssh", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1}
	ctx := executor.WithRunSSH(context.Background(), hostsupervisor.RunRequest{RunID: "run", Topology: "lab"})
	ctx = substrate.WithOperationIdentity(ctx, identity)
	_, err := transport.NewTaskClient(nil, "provider", 1).MutateSSHAccess(ctx, substrate.SSHAccessMutation{ID: "ssh", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}})
	require.ErrorContains(t, err, "parent grant unavailable")
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
			executor := RunExecutor{Bridge: bridge, Tasks: &hostsupervisor.Dispatcher{AgentID: "agent"}}
			_, err := executor.Execute(context.Background(), hostsupervisor.RunRequest{RunID: "run", Topology: "lab", Revision: "revision", Action: "apply"})
			require.ErrorContains(t, err, "canonical claim changed")
			require.False(t, bridge.materialized)
		})
	}
}
