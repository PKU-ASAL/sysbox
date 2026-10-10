package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestSourceGrantSupportsLifecycleRuns(t *testing.T) {
	s := guestExecutionTestServer(t)
	ctx := context.Background()
	for _, action := range []string{"apply", "destroy", "reset"} {
		t.Run(action, func(t *testing.T) {
			run := controlplane.Run{ID: "run-" + action, Topology: "lab", Revision: "sha256:revision", Op: action, Operation: action, Target: "sysbox_node.web", Status: controlplane.RunAssigned, AgentID: "agent", LeaseOwner: "lease", LeaseUntil: time.Now().Add(time.Minute), Attempt: 2}
			cmd := controlplane.AgentCommand{ID: "cmd-" + action, AgentID: "agent", Type: "run_assigned", Status: controlplane.AgentCommandStatusRunning, Run: &run, ActionID: "action-" + action, TopologyUID: "topology", ResourceUID: "resource", Generation: 3, ExpectedSerial: 1, LeaseOwner: "lease", Attempt: 2, LeaseUntil: run.LeaseUntil}
			record := &persistence.CommandRecord{Command: cmd}
			req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: cmd.TopologyUID, Revision: run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: run.Target, ExpectedSerial: cmd.ExpectedSerial}
			digest, err := req.Digest()
			require.NoError(t, err)
			grant, err := s.sourceGrant(ctx, "agent", ExecutionGrantRequest{SessionID: "session", LeaseOwner: "lease", Attempt: 2, BootID: "host"}, record)
			require.NoError(t, err)
			require.Equal(t, "run_"+action, grant.Operation)
			require.Equal(t, digest, grant.Digest)
		})
	}
}
