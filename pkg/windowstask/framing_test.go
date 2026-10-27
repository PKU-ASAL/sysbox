package windowstask

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWindowsTaskFramingRejectsTrailingAndUnknownData(t *testing.T) {
	for _, raw := range []string{`{"version":1,"operation":"info"} {}`, `{"version":1,"operation":"info","untrusted":true}`} {
		_, err := ReadCall(strings.NewReader(raw))
		require.Error(t, err)
	}
	_, err := ReadCall(strings.NewReader(`{"version":1,"operation":"info"}`))
	require.NoError(t, err)
}
