package persistence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunMaintenanceRejectsUnknownOwnerDuringFreeze(t *testing.T) {
	ctx := context.Background()
	r := NewFilesystemExecutionGrantRepository(t.TempDir())
	a := newAggregate()
	barrier, err := r.BeginMaintenance(ctx, "freeze", a.State.TopologyUID, "plan")
	require.NoError(t, err)
	raw, err := r.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateRunMaintenance(raw, nil, "lab"), ErrConflict)
	lifecycle, err := json.Marshal(a)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateRunMaintenance(raw, lifecycle, "lab"), ErrConflict)
	other, err := json.Marshal(newAggregate())
	require.NoError(t, err)
	require.NoError(t, ValidateRunMaintenance(raw, other, "other"))
	require.ErrorIs(t, ValidateRunMaintenance([]byte(`{}`), nil, "lab"), ErrCorruptRecord)
	require.Error(t, ValidateRunMaintenance(nil, []byte(`{"state":{"topology_uid":"fake"}}`), "lab"))
	require.NoError(t, r.EndMaintenance(ctx, *barrier))
	raw, err = r.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	require.NoError(t, ValidateRunMaintenance(raw, nil, "lab"))
}
