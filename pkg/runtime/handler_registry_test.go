package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegisterResourceHandlerRejectsDuplicateType(t *testing.T) {
	registry := newHandlerRegistry()
	handler := observationProvider{}
	require.NoError(t, registry.Register(handler))
	require.ErrorContains(t, registry.Register(handler), "already registered")
}
