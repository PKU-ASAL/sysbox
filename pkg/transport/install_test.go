package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"io"
	"path/filepath"
	"testing"
)

type installConnection struct {
	result      substrate.ExecResult
	request     substrate.ExecRequest
	temporary   string
	preparation substrate.ExecRequest
}

func (c *installConnection) CopyFile(_ context.Context, _, dst string) error {
	c.temporary = dst
	return nil
}
func (c *installConnection) Exec(_ context.Context, r substrate.ExecRequest, _, _ io.Writer) (substrate.ExecResult, error) {
	if r.Program == "mkdir" {
		c.preparation = r
		return substrate.ExecResult{ExitCodeKnown: true}, nil
	}
	c.request = r
	return c.result, nil
}

func TestInstallFileRequiresKnownSuccessfulCompletion(t *testing.T) {
	c := &installConnection{}
	require.Error(t, InstallFile(context.Background(), c, "src", "/tmp/target", 0600))
	first := c.temporary
	c.result = substrate.ExecResult{ExitCodeKnown: true}
	require.NoError(t, InstallFile(context.Background(), c, "src", "/tmp/target", 0600))
	require.NotEqual(t, first, c.temporary, "concurrent transfers must not overwrite each other's temporary file")
	require.Contains(t, c.request.Program, "mv -fT")
	require.Contains(t, c.request.Program, "sync -f")
	require.Equal(t, []string{"-m", "0700", "--", filepath.Dir(c.temporary)}, c.preparation.Args)
	c.result.ExitCode = 1
	require.Error(t, InstallFile(context.Background(), c, "src", "/tmp/target", 0600))
}
