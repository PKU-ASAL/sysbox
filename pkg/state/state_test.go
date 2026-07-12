package state

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStateRoundTrip(t *testing.T) {
	original := &State{
		Version: SchemaVersion,
		Lineage: "test-run-01",
		Resources: []Resource{
			{
				Address: address.Resource("sysbox_node", "web"),
				Driver:  "docker",
				Attributes: map[string]any{
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
	require.Equal(t, original.Lineage, decoded.Lineage)
	require.Len(t, decoded.Resources, 1)
	require.Equal(t, "web", decoded.Resources[0].Address.Name)
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
			{Address: address.Resource("sysbox_node", "web")},
			{Address: address.Resource("sysbox_node", "db")},
		},
	}

	r := s.FindResource(address.Resource("sysbox_node", "web"))
	require.NotNil(t, r)
	require.Equal(t, "web", r.Address.Name)

	require.Nil(t, s.FindResource(address.Resource("sysbox_node", "notfound")))
}

func TestStateRemoveResource(t *testing.T) {
	s := &State{
		Resources: []Resource{
			{Address: address.Resource("sysbox_node", "web")},
			{Address: address.Resource("sysbox_node", "db")},
		},
	}
	s.RemoveResource(address.Resource("sysbox_node", "web"))
	require.Len(t, s.Resources, 1)
	require.Equal(t, "db", s.Resources[0].Address.Name)
}

func TestManagerSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	mgr := NewManager(path)

	s := &State{Version: SchemaVersion, Lineage: "r1", Resources: []Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "docker", Attributes: map[string]any{"id": "abc"}},
	}}

	require.NoError(t, mgr.Save(s))

	loaded, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, "r1", loaded.Lineage)
	require.Len(t, loaded.Resources, 1)
}

func TestManagerSaveCreatesNestedStateDirBeforeLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "two-networks", "state.json")
	mgr := NewManager(path)

	s := &State{Version: SchemaVersion, Lineage: "r1"}

	require.NoError(t, mgr.Save(s))
	require.FileExists(t, path)
}

func TestManagerLoadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "state.json"))

	s, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, 0, len(s.Resources))
	require.False(t, s.Meta.Exists)
}

func TestManagerVersionedBackendUsesCAS(t *testing.T) {
	backend := &recordingVersionedBackend{
		loaded: &LoadedState{
			Data:      []byte(`{"version":5,"lineage":"r1","resources":[]}`),
			Metadata:  Metadata{Backend: "test", Serial: 7},
			Exists:    true,
			Serial:    7,
			UpdatedAt: timeNowUTC(),
		},
	}
	mgr := NewManagerWithBackend(backend)

	st, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, int64(7), st.Meta.Serial)

	require.NoError(t, mgr.Save(st))
	require.True(t, backend.lastSave.RequireCAS)
	require.Equal(t, int64(7), backend.lastSave.ExpectedSerial)
}

func TestManagerOptionalBackendOperationsAreNoopsWhenUnsupported(t *testing.T) {
	mgr := NewManagerWithBackend(noopOptionalBackend{})

	require.NoError(t, mgr.Delete(context.Background()))
	require.NoError(t, mgr.ForceUnlock(context.Background()))

	info, err := mgr.LockInfo(context.Background())
	require.NoError(t, err)
	require.False(t, info.Locked)
}

type noopOptionalBackend struct{}

func (noopOptionalBackend) Load(context.Context) ([]byte, error) { return nil, nil }
func (noopOptionalBackend) Save(context.Context, []byte) error   { return nil }
func (noopOptionalBackend) Lock(context.Context) (UnlockFunc, error) {
	return nil, nil
}

type recordingVersionedBackend struct {
	loaded   *LoadedState
	lastSave SaveOptions
}

func (*recordingVersionedBackend) Capabilities() BackendCapabilities {
	return BackendCapabilities{Locking: true, CAS: true}
}

func (b *recordingVersionedBackend) Load(context.Context) ([]byte, error) {
	if b.loaded == nil || !b.loaded.Exists {
		return nil, nil
	}
	return b.loaded.Data, nil
}

func (b *recordingVersionedBackend) Save(context.Context, []byte) error { return nil }
func (b *recordingVersionedBackend) Lock(context.Context) (UnlockFunc, error) {
	return nil, nil
}

func (b *recordingVersionedBackend) LoadVersioned(context.Context) (*LoadedState, error) {
	return b.loaded, nil
}

func (b *recordingVersionedBackend) SaveVersioned(_ context.Context, _ []byte, opts SaveOptions) error {
	b.lastSave = opts
	b.loaded.Serial++
	b.loaded.Metadata.Serial = b.loaded.Serial
	b.loaded.Exists = true
	return nil
}

func timeNowUTC() time.Time {
	return time.Now().UTC()
}
