package main

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMatrixReadsCanonicalRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	repo, err := persistence.OpenFilesystem(path + ".lifecycle")
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(context.Background(), "heterogeneous-matrix")
	require.NoError(t, err)
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_network", "matrix"), OwnerUID: st.TopologyUID, ResourceUID: "network", Generation: 1}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("heterogeneous-matrix")).Save(st))
	got, err := loadMatrixState(path)
	require.NoError(t, err)
	require.Equal(t, st.TopologyUID, got.TopologyUID)
	require.NotNil(t, got.FindResource(address.Resource("sysbox_network", "matrix")))
}

func TestMatrixReadsExplicitTopologyWithoutInitializingDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	repo, err := persistence.OpenFilesystem(path + ".lifecycle")
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(context.Background(), "nat-matrix")
	require.NoError(t, err)
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_network", "matrix"), OwnerUID: st.TopologyUID, ResourceUID: "network", Generation: 1}}
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("nat-matrix")).Save(st))
	got, err := loadTopologyState(path, "nat-matrix")
	require.NoError(t, err)
	require.Equal(t, st.TopologyUID, got.TopologyUID)
	require.NoFileExists(t, filepath.Join(path+".lifecycle", "heterogeneous-matrix.json"))
}
