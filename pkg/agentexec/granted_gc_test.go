package agentexec

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestGCRequiresOwningHostAndCompleteLeaseBinding(t *testing.T) {
	cmd := controlplane.AgentCommand{Type: "gc_delete", ID: "gc", AgentID: "agent"}
	require.Error(t, runGrantedGC(context.Background(), Options{ID: "agent"}, cmd))
	require.Error(t, runGrantedGC(context.Background(), Options{ID: "other", HostSupervisorSocket: "/absent", ExecutionSessionID: "session", ExecutionGrantPublicKeyFile: "/absent"}, cmd))
}
