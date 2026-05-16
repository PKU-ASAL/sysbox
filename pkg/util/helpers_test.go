package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsString(t *testing.T) {
	require.Equal(t, "hello", AsString("hello"))
	require.Equal(t, "", AsString(nil))
	require.Equal(t, "", AsString(42))
	require.Equal(t, "", AsString(map[string]any{}))
	require.Equal(t, "", AsString(""))
}
