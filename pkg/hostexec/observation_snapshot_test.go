package hostexec

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type snapshotBackend struct {
	state.Backend
	data          []byte
	metadataCalls int
	loadedAt      time.Time
}

func (b *snapshotBackend) LoadVersioned(ctx context.Context) (*state.LoadedState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.loadedAt = time.Now()
	return &state.LoadedState{Data: b.data, Exists: true, Serial: 7}, nil
}
func (b *snapshotBackend) SaveVersioned(context.Context, []byte, state.SaveOptions) error { return nil }
func (b *snapshotBackend) Metadata(context.Context) (state.Metadata, error) {
	b.metadataCalls++
	return state.Metadata{Serial: 8}, nil
}

type snapshotBridge struct {
	Bridge
	manager *state.Manager
}

func (b snapshotBridge) StateManager(string) (*state.Manager, error) { return b.manager, nil }

func TestObservationVersionAndTimestampDescribeLoadedSnapshot(t *testing.T) {
	data, err := (&state.State{Version: state.SchemaVersion}).Marshal()
	require.NoError(t, err)
	backend := &snapshotBackend{data: data}
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.json"))
	mgr.SetBackend(backend)
	bridge := snapshotBridge{manager: mgr}
	projection, err := ObserveTopology(context.Background(), "host-a", bridge, "lab")
	require.NoError(t, err)
	require.Equal(t, int64(7), projection.Serial)
	require.Zero(t, backend.metadataCalls, "do not label old resources with separately read newer metadata")
	require.False(t, projection.ObservedAt.After(backend.loadedAt), "slow probes must retain the original sampling timestamp")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ObserveTopology(ctx, "host-a", bridge, "lab")
	require.ErrorIs(t, err, context.Canceled)
}
