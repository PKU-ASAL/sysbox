package runtime

import (
	"github.com/stretchr/testify/require"
	"reflect"
	"testing"
)

func TestHandlersCannotExecuteCheckpointRecovery(t *testing.T) {
	types := RegisteredResourceTypes()
	require.Contains(t, types, "sysbox_node")
	for _, kind := range types {
		handler, _ := GetResourceHandler(kind)
		for _, method := range []string{"RecoverCheckpointResource", "CleanupCheckpointResource"} {
			_, exists := reflect.TypeOf(handler).MethodByName(method)
			require.False(t, exists, "%s exposes a second recovery authority via %s", kind, method)
		}
	}
}

func TestRecoveryCoverageUsesManagedDiscovery(t *testing.T) {
	types := RegisteredResourceTypes()
	require.NotEmpty(t, types)
	for _, kind := range types {
		handler, _ := GetResourceHandler(kind)
		if kind == "sysbox_ssh_access" {
			continue
		} // Explicitly unsupported until a read-only recovery proof exists.
		_, ok := handler.(ResourceDiscoverer)
		require.True(t, ok, "%s requires managed discovery for canonical recovery", kind)
	}
}
