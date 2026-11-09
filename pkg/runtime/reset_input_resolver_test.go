package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/stretchr/testify/require"
)

func TestResetProviderRequestResolvesTransientInputWithoutPersistingIt(t *testing.T) {
	sub := &portTestSubstrate{name: "reset-input", coldPlug: true}
	registerPortTestDriver(t, sub)
	g, st := configuredResetTestState(t, sub, "first")
	node := g.Get(address.Resource("sysbox_node", "first"))
	node.Data.(*config.NodeConfig).Env = map[string]string{"PASSWORD": "secret://input/password"}
	plan, err := BuildResetPlan(g, st, "sysbox_node.first")
	require.NoError(t, err)
	executor := testActionExecutor(t, g, st)
	executor.SetSecretResolver(secret.Dispatcher{"input": secret.InputResolver{Inputs: map[string]string{"password": "private-canary"}}})
	item, err := executor.buildResetNodeContext(context.Background(), plan.Actions[0])
	require.NoError(t, err)
	require.Equal(t, "private-canary", item.request.Node.Env["PASSWORD"])
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-canary")
	executor.SetSecretResolver(secret.Dispatcher{"input": secret.InputResolver{}})
	_, err = executor.buildResetNodeContext(context.Background(), plan.Actions[0])
	require.Error(t, err)
	require.Empty(t, sub.resetLifecycle, "missing transient input must fail before destructive provider effects")
}
