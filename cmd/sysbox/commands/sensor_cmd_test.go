package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/util"
)

func TestMonitorsTargets_ResolvesFromState(t *testing.T) {
	st := &state.State{
		Resources: []state.Resource{
			{
				Type: "sysbox_node", Name: "node_attack", Provider: "docker",
				Instance: map[string]any{"container_id": "cid-attack"},
			},
			{
				Type: "sysbox_node", Name: "node_web", Provider: "docker",
				Instance: map[string]any{"container_id": "cid-web"},
			},
		},
	}
	m := state.Resource{
		Type:     "sysbox_monitor",
		Name:     "lab",
		Provider: "monitor",
		Instance: map[string]any{
			"nodes": []any{"node_attack", "node_web"},
		},
	}

	targets := monitorsTargets(m, st)
	require.Len(t, targets, 2)

	byID := map[string]map[string]string{}
	for _, t := range targets {
		byID[t.NodeID] = t.Handle
	}
	require.Equal(t, "cid-attack", byID["node_attack"]["container_id"])
	require.Equal(t, "sysbox-node_attack", byID["node_attack"]["container_name"])
	require.Equal(t, "cid-web", byID["node_web"]["container_id"])
}

func TestMonitorsTargets_SkipsMissingNodes(t *testing.T) {
	st := &state.State{
		Resources: []state.Resource{
			{
				Type: "sysbox_node", Name: "node_attack", Provider: "docker",
				Instance: map[string]any{"container_id": "cid-1"},
			},
		},
	}
	m := state.Resource{
		Instance: map[string]any{
			"nodes": []any{"node_attack", "node_missing"},
		},
	}

	targets := monitorsTargets(m, st)
	require.Len(t, targets, 1)
	require.Equal(t, "node_attack", targets[0].NodeID)
}

func TestMonitorConfig_ReconstructsFromInstance(t *testing.T) {
	m := state.Resource{
		Instance: map[string]any{
			"events": []any{"execve", "openat"},
			"extra": map[string]any{
				"sensor_container": "sysbox-sensor",
				"tracee_bin":       "/tracee/tracee",
				// Non-string values must be silently dropped to keep the
				// reconstructed Config consistent with its declared shape.
				"ignored": 42,
			},
		},
	}

	cfg := monitorConfig(m)
	require.Equal(t, []string{"execve", "openat"}, cfg.Events)
	require.Equal(t, "sysbox-sensor", cfg.Extra["sensor_container"])
	require.Equal(t, "/tracee/tracee", cfg.Extra["tracee_bin"])
	require.NotContains(t, cfg.Extra, "ignored")
}

func TestMonitorConfig_EmptyInstance(t *testing.T) {
	cfg := monitorConfig(state.Resource{Instance: map[string]any{}})
	require.Empty(t, cfg.Events)
	require.Empty(t, cfg.Extra)
}

func TestAsStringFromMap_NilSafe(t *testing.T) {
	require.Equal(t, "", util.AsString(nil))
	require.Equal(t, "v", util.AsString(map[string]any{"k": "v"}["k"]))
	require.Equal(t, "", util.AsString(map[string]any{"k": 42}["k"]))
}
