package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type unsafeBackend struct{}

func (unsafeBackend) Load(context.Context) ([]byte, error)     { return nil, nil }
func (unsafeBackend) Save(context.Context, []byte) error       { return nil }
func (unsafeBackend) Lock(context.Context) (UnlockFunc, error) { return nil, nil }
func (unsafeBackend) Capabilities() BackendCapabilities        { return BackendCapabilities{} }

func TestBackendCapabilitiesClassifyMutationSafety(t *testing.T) {
	require.True(t, (&LocalBackend{}).Capabilities().SafeMutation())
	require.True(t, (&SQLiteBackend{}).Capabilities().SafeMutation())
	require.True(t, (&PostgresBackend{}).Capabilities().SafeMutation())
	require.False(t, (&HTTPBackend{}).Capabilities().SafeMutation())
	require.False(t, (&S3Backend{}).Capabilities().SafeMutation())
}

func TestManagerRejectsUnsafeMutationUnlessExplicitlyAllowed(t *testing.T) {
	manager := NewManagerWithBackend(unsafeBackend{})
	require.ErrorContains(t, manager.RequireMutationSafety(false), "unsafe state backend")
	require.NoError(t, manager.RequireMutationSafety(true))
	require.ErrorContains(t, manager.Save(&State{Version: SchemaVersion}), "unsafe state backend")
	manager.AllowUnsafeMutation(true)
	require.NoError(t, manager.Save(&State{Version: SchemaVersion}))
}
