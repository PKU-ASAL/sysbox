package agentexec

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestConsoleRequiresSupervisorWithoutProviderFallback(t *testing.T) {
	_, err := startGrantedTask(context.Background(), Options{}, controlplane.AgentCommand{Type: "session_open"})
	require.ErrorContains(t, err, "host supervisor socket")
}
