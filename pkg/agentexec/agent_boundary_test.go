package agentexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentCannotRunWithoutHostBoundary(t *testing.T) {
	require.ErrorContains(t, Run(context.Background(), Options{APIURL: "http://unused"}), "host supervisor socket")
	require.ErrorContains(t, Run(context.Background(), Options{APIURL: "http://unused", HostSupervisorSocket: "/unused", Token: "administrator"}), "administrator API token")
	require.ErrorContains(t, Run(context.Background(), Options{APIURL: "http://unused", HostSupervisorSocket: "/unused"}), "pinned host public key")
}
