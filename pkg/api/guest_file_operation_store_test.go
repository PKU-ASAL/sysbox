package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestGuestFileOperationStoresPersistPrivateBindingAndCAS(t *testing.T) {
	ctx := context.Background()
	stores := map[string]guestFileOperationPersistence{
		"local":  &localAPIStore{runsDir: t.TempDir()},
		"sqlite": &sqliteAPIStore{dbPath: filepath.Join(t.TempDir(), "api.db"), runsDir: t.TempDir()},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			want := controlplane.GuestFileOperation{ID: "file-1", Version: 1, AgentID: "host-a", CommandID: "cmd-private", Topology: "lab", Node: "web", Status: controlplane.GuestExecutionQueued}
			require.NoError(t, store.SaveGuestFileOperation(ctx, want))
			got, err := store.GetGuestFileOperation(ctx, want.ID)
			require.NoError(t, err)
			require.Equal(t, want, *got)
			got.Status = controlplane.GuestExecutionRunning
			ok, err := store.CompareAndSwapGuestFileOperation(ctx, *got, 1)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.CompareAndSwapGuestFileOperation(ctx, *got, 1)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}
