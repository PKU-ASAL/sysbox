package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

type unavailableConsoleInventory struct{ apiStore }

func (unavailableConsoleInventory) ListConsoleSessions(context.Context, string) ([]controlplane.ConsoleSession, error) {
	return nil, errors.New("console inventory unavailable")
}

func TestAPIDoesNotStartWithUnreadableConsoleRegistry(t *testing.T) {
	runs := t.TempDir()
	path := filepath.Join(runs, "lab", "sessions", "broken.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	raw := []byte(`{broken`)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	s := NewServer(runs, t.TempDir())
	require.ErrorContains(t, s.initErr, "initialize console registry")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, s.StartContext(ctx, "127.0.0.1:0"), "initialization failure cannot be hidden by cancellation")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, after)
}

func TestConsoleStartupDoesNotTreatFailedInventoryAsEmpty(t *testing.T) {
	hub := newConsoleSessionHub(unavailableConsoleInventory{})
	_, err := hub.Snapshot("existing")
	require.ErrorContains(t, err, "console inventory unavailable")
	_, err = hub.Create(context.Background(), "lab", "node", "agent", controlplane.ConsoleRequest{})
	require.ErrorContains(t, err, "console inventory unavailable")
}

func TestConsoleStartupDoesNotPublishUncommittedLostState(t *testing.T) {
	store := &localAPIStore{runsDir: t.TempDir()}
	original := newConsoleSessionHub(store)
	sess := createConsoleForTest(t, original)
	hub := newConsoleSessionHub(failingConsoleSave{apiStore: store})
	_, err := hub.Snapshot(sess.ID)
	require.ErrorContains(t, err, "storage unavailable")
	require.Empty(t, hub.sessions)
	persisted, err := store.GetConsoleSession(context.Background(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, controlplane.ConsoleSessionStatusQueued, persisted.Status)
}
