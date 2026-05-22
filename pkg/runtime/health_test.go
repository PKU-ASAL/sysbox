package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
)

func TestEvaluateTopologyHealthEmptyState(t *testing.T) {
	health := EvaluateTopologyHealth(context.Background(), &state.State{Version: state.SchemaVersion})

	require.Equal(t, ResourceHealthHealthy, health.Status)
	require.Empty(t, health.Resources)
	require.Zero(t, health.Drifted)
	require.Zero(t, health.Unknown)
}

func TestEvaluateTopologyHealthKernelFile(t *testing.T) {
	dir := t.TempDir()
	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, writeTestFile(kernel))
	st := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_kernel",
			Name:     "linux",
			Provider: "artifact",
			Instance: map[string]any{"path": kernel},
		}},
	}

	health := EvaluateTopologyHealth(context.Background(), st)

	require.Equal(t, ResourceHealthHealthy, health.Status)
	require.Equal(t, ResourceHealthHealthy, health.Resources[0].Status)
	require.True(t, health.Resources[0].Checks["file"].OK)
}

func TestEvaluateTopologyHealthMissingKernelDrifts(t *testing.T) {
	st := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Type:     "sysbox_kernel",
			Name:     "linux",
			Provider: "artifact",
			Instance: map[string]any{"path": filepath.Join(t.TempDir(), "missing")},
		}},
	}

	health := EvaluateTopologyHealth(context.Background(), st)

	require.Equal(t, ResourceHealthDrifted, health.Status)
	require.Equal(t, 1, health.Drifted)
	require.Equal(t, ResourceHealthDrifted, health.Resources[0].Status)
	require.Equal(t, RecoveryDecisionMarkDrift, health.Resources[0].Decision)
}

func TestEvaluateResourceHealthUnsupportedResourceIsHealthyUnknownProbe(t *testing.T) {
	res := &state.Resource{
		Type:     "sysbox_image",
		Name:     "alpine",
		Provider: "docker",
		Instance: map[string]any{"repository": "alpine:latest"},
	}

	health := EvaluateResourceHealth(context.Background(), res)

	require.Equal(t, ResourceHealthHealthy, health.Status)
	require.Equal(t, "resource has no runtime health probe", health.Reason)
}

func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("test"), 0o644)
}
