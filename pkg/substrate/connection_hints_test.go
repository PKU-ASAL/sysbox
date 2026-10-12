package substrate

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/stretchr/testify/require"
)

func TestCanonicalConnectionHintsPreserveTransportAndResolveSecrets(t *testing.T) {
	desired := map[string]any{"connections": []config.ConnectionConfig{{Type: "ssh", Host: "192.0.2.10", User: "ubuntu", PrivateKey: "secret://input/key", Password: "secret://input/password", KnownHosts: "/trusted/known_hosts"}}}
	ctx := secret.WithResolver(context.Background(), secret.InputResolver{Inputs: map[string]string{"key": "private-test-key", "password": "test-password"}})
	hints, err := CanonicalConnectionHints(ctx, desired)
	require.NoError(t, err)
	require.Equal(t, []ConnectionHint{{Type: "ssh", Host: "192.0.2.10", User: "ubuntu", PrivateKey: "private-test-key", Password: "test-password", KnownHosts: "/trusted/known_hosts"}}, hints)
	require.Equal(t, "secret://input/key", desired["connections"].([]config.ConnectionConfig)[0].PrivateKey)
	_, err = CanonicalConnectionHints(context.Background(), desired)
	require.Error(t, err, "missing secret must not silently change transport or trust")
}
