package substrate

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestFileInstallCanonicalRequest(t *testing.T) {
	r := FileInstallRequest{ID: "install", Path: "/tmp/file", Mode: 0640, Size: 3, SHA256: strings.Repeat("a", 64)}
	before, err := r.Digest()
	require.NoError(t, err)
	r.Mode = 0600
	after, err := r.Digest()
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	for _, path := range []string{"relative", "/", "/tmp/../etc/file", "/etc/sysbox/grant-public-key", "/proc/self/mem"} {
		r.Path = path
		require.Error(t, r.Validate())
	}
}
