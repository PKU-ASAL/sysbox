package transport

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSSHCopyVerifiesContentBeforeAtomicReplacement(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	dst := filepath.Join(t.TempDir(), "target with 'quotes'")
	require.NoError(t, os.WriteFile(src, []byte("payload\x00\n"), 0640))
	// The transfer preserves source mode; make the fixture independent of umask.
	require.NoError(t, os.Chmod(src, 0640))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0600))
	conn := NewSSHConnectionSecure("unused", "22", "root", "", "")
	conn.runCommand = func(cmd *exec.Cmd) error {
		local := exec.Command("sh", "-c", cmd.Args[len(cmd.Args)-1])
		local.Stdin, local.Stdout, local.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
		return local.Run()
	}
	validRunner := conn.runCommand
	require.NoError(t, conn.CopyFile(context.Background(), src, dst))
	actual, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload\x00\n", string(actual))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0640), info.Mode().Perm())
	conn.runCommand = func(cmd *exec.Cmd) error {
		local := exec.Command("sh", "-c", cmd.Args[len(cmd.Args)-1])
		reader, writer := io.Pipe()
		go func() { fmt.Fprint(writer, "corrupt"); writer.Close() }()
		local.Stdin, local.Stdout, local.Stderr = reader, cmd.Stdout, cmd.Stderr
		defer reader.Close()
		return local.Run()
	}
	require.Error(t, conn.CopyFile(context.Background(), src, dst))
	actual, err = os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload\x00\n", string(actual))
	entries, err := os.ReadDir(filepath.Dir(dst))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	conn.runCommand = validRunner
	require.Error(t, conn.CopyFile(context.Background(), src, filepath.Dir(dst)), "directory target must not be reported as an installed file")
}
