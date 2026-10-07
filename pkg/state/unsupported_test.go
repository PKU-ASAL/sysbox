package state

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUnsupportedManagementCapabilitiesNeverReturnSuccess(t *testing.T) {
	m := NewManagerWithBackend(&HTTPBackend{URL: "http://unused"})
	_, err := m.Snapshot(context.Background(), "test")
	require.Error(t, err)
	require.Error(t, m.ForceUnlock(context.Background()))
	_, err = m.LockInfo(context.Background())
	require.Error(t, err)
	_, err = m.ListTopologies(context.Background())
	require.Error(t, err)
}
