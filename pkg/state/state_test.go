package state

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateRoundTrip(t *testing.T) {
	original := &State{
		Version: SchemaVersion,
		RunID:   "test-run-01",
		Resources: []Resource{
			{
				Type:     "sysbox_node",
				Name:     "web",
				Provider: "docker",
				Instance: map[string]any{
					"id":      "container-abc123",
					"image":   "alpine:3.19",
					"running": true,
				},
			},
		},
	}

	bytes, err := original.Marshal()
	require.NoError(t, err)

	decoded, err := Unmarshal(bytes)
	require.NoError(t, err)
	require.Equal(t, original.RunID, decoded.RunID)
	require.Len(t, decoded.Resources, 1)
	require.Equal(t, "web", decoded.Resources[0].Name)
}

func TestUnmarshalRejectsV1(t *testing.T) {
	// A v1 state file (sysbox v0.x). Loading must fail with IncompatibleVersionError.
	v1 := []byte(`{"version":1,"run_id":"old","resources":[]}`)
	_, err := Unmarshal(v1)
	require.Error(t, err)
	var ve *IncompatibleVersionError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, 1, ve.Found)
	require.Equal(t, SchemaVersion, ve.Expected)
}

func TestStateFindResource(t *testing.T) {
	s := &State{
		Resources: []Resource{
			{Type: "sysbox_node", Name: "web"},
			{Type: "sysbox_node", Name: "db"},
		},
	}

	r := s.FindResource("sysbox_node", "web")
	require.NotNil(t, r)
	require.Equal(t, "web", r.Name)

	require.Nil(t, s.FindResource("sysbox_node", "notfound"))
}

func TestStateRemoveResource(t *testing.T) {
	s := &State{
		Resources: []Resource{
			{Type: "sysbox_node", Name: "web"},
			{Type: "sysbox_node", Name: "db"},
		},
	}
	s.RemoveResource("sysbox_node", "web")
	require.Len(t, s.Resources, 1)
	require.Equal(t, "db", s.Resources[0].Name)
}

func TestManagerSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	mgr := NewManager(path)

	s := &State{Version: SchemaVersion, RunID: "r1", Resources: []Resource{
		{Type: "sysbox_node", Name: "web", Provider: "docker", Instance: map[string]any{"id": "abc"}},
	}}

	require.NoError(t, mgr.Save(s))

	loaded, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, "r1", loaded.RunID)
	require.Len(t, loaded.Resources, 1)
}

func TestManagerLoadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "state.json"))

	s, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, 0, len(s.Resources))
}
