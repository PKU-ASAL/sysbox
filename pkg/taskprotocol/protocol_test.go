package taskprotocol

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPortableTaskDigestPreservesLinuxWireContract(t *testing.T) {
	r := Request{ID: "task-golden", UserID: 65534, Args: []string{"cmd.exe", "/c", "echo hi"}, OutputLimit: 1024}
	got, err := r.Digest()
	require.NoError(t, err)
	require.Equal(t, "398a6e3ac721684364b04c2d6c86bb4fe8cf12b4d5e93628e5bb30a25c659b04", got)
}
