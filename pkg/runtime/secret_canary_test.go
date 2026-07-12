package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
)

func TestSecretCanaryDoesNotEnterDurableArtifacts(t *testing.T) {
	const canary = "plaintext-canary-must-never-persist"
	previousResolver := executionSecretResolver
	executionSecretResolver = secret.EnvironmentResolver{Lookup: func(string) (string, bool) { return canary, true }}
	t.Cleanup(func() { executionSecretResolver = previousResolver })
	resolved, err := resolveSecretMap(context.Background(), map[string]string{"TOKEN": "secret://env/CANARY_TOKEN"})
	require.NoError(t, err)
	require.Equal(t, canary, resolved["TOKEN"])
	addr := address.Resource("sysbox_node", "web")
	node := &graph.Node{Address: addr, Data: &config.NodeConfig{Image: "sysbox_image.base", Substrate: "docker", Env: map[string]string{"TOKEN": "secret://env/CANARY_TOKEN"}}}
	attributes := map[string]any{}
	require.NoError(t, setDesiredHash(node, attributes))
	st := &state.State{Version: state.SchemaVersion, Resources: []state.Resource{{Address: addr, Attributes: state.MustAttributes(attributes)}}}
	stateJSON, err := st.Marshal()
	require.NoError(t, err)
	planJSON, err := json.Marshal(Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionCreate}}})
	require.NoError(t, err)
	checkpointJSON, err := json.Marshal(OperationCheckpoint{Topology: "lab", Operation: "apply"})
	require.NoError(t, err)
	for name, artifact := range map[string][]byte{"state": stateJSON, "plan": planJSON, "checkpoint": checkpointJSON} {
		t.Run(name, func(t *testing.T) {
			require.False(t, strings.Contains(string(artifact), canary))
			require.NotContains(t, string(artifact), `"TOKEN":"`+canary+`"`)
		})
	}
}
