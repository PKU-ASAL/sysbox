package runtime

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/stretchr/testify/require"
)

func TestResolveConnectionHintsResolvesSecretAtExecutionTime(t *testing.T) {
	exec := &Executor{}
	exec.SetSecretResolver(secret.EnvironmentResolver{Lookup: func(name string) (string, bool) { return "resolved-password", name == "FC_SSH_PASSWORD" }})
	hints, err := exec.resolveConnectionHints(context.Background(), []config.ConnectionConfig{{Type: "ssh", User: "ubuntu", Password: "secret://env/FC_SSH_PASSWORD", KnownHosts: "/etc/sysbox/known_hosts"}})
	require.NoError(t, err)
	require.Equal(t, "resolved-password", hints[0].Password)
	require.Equal(t, "/etc/sysbox/known_hosts", hints[0].KnownHosts)
}
