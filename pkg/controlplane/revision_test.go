package controlplane

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeRevisionDigestDeterministic(t *testing.T) {
	files := map[string][]byte{
		"field.sysbox.hcl":      []byte(`resource "sysbox_node" "web" {}`),
		"modules/web/main.hcl":  []byte(`resource "sysbox_node" "web" {}`),
		"files/playbook.tar.gz": []byte("binary-bytes"),
	}

	first := ComputeRevisionDigest(files)
	second := ComputeRevisionDigest(files)

	require.Equal(t, first, second, "digest must be stable across calls")
	require.True(t, strings.HasPrefix(first, "sha256:"))
	require.Len(t, first, len("sha256:")+64)
}

func TestComputeRevisionDigestIncludesPaths(t *testing.T) {
	require.NotEqual(t,
		ComputeRevisionDigest(map[string][]byte{"a": []byte("x")}),
		ComputeRevisionDigest(map[string][]byte{"b": []byte("x")}),
		"same content at different paths must produce different digests")
}

func TestComputeRevisionDigestLengthPrefixed(t *testing.T) {
	require.NotEqual(t,
		ComputeRevisionDigest(map[string][]byte{"ab": []byte("c")}),
		ComputeRevisionDigest(map[string][]byte{"a": []byte("bc")}),
		"length prefixes must prevent concatenation collisions")
}
