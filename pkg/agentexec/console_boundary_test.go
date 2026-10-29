package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConsoleRequiresSupervisorWithoutProviderFallback(t *testing.T) {
	_, err := startGrantedTask(context.Background(), Options{}, controlplane.AgentCommand{Type: "session_open"})
	require.ErrorContains(t, err, "host supervisor socket")
}
