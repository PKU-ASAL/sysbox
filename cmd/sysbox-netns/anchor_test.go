package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnchorNameRejectsPathsAndOptions(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../other", "a/b", "-all", "a b", "a\n", "a\\b"} {
		require.Error(t, validateAnchorName(name), name)
	}
	require.NoError(t, validateAnchorName("sysbox-net-lab-test"))
}

func TestAnchorIdentityRejectsOrdinaryFileAndSymlink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "namespace")
	require.NoError(t, os.WriteFile(p, nil, 0600))
	_, _, err := anchorIdentity(p)
	require.Error(t, err)
	require.NoError(t, os.Symlink("/proc/self/ns/net", p+"-link"))
	_, _, err = anchorIdentity(p + "-link")
	require.Error(t, err)
}

func TestAnchorRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary user")
	}
	var stderr bytes.Buffer
	require.Equal(t, 1, runAnchor([]string{"--anchor-create", "test"}, io.Discard, &stderr))
	require.Contains(t, stderr.String(), "requires root")
}
