package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertProjectMaterializesWholeTree(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	files := map[string][]byte{
		"field.sysbox.hcl":     []byte("root"),
		"modules/web/main.hcl": []byte("module"),
		"files/run.txt":        []byte("file"),
	}
	require.NoError(t, s.workspaceService().UpsertProject(context.Background(), "proj", files))

	base := filepath.Dir(s.workspaceService().HCLFile("proj"))
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(p)))
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "content mismatch at %s", p)
	}
}

func TestUpsertProjectRejectsTraversal(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	for _, p := range []string{"../evil", "a/../../evil", "/abs", `a\b`, "", "a//b", ".", "..", "a/./b", "a/../b"} {
		err := s.workspaceService().UpsertProject(context.Background(), "proj", map[string][]byte{p: []byte("x")})
		require.Error(t, err, "path %q must be rejected", p)
	}
}

func TestValidateRelPath(t *testing.T) {
	for _, p := range []string{"field.sysbox.hcl", "modules/web/main.hcl", "files/run.txt", "a.b/c-d_e"} {
		require.NoError(t, validateRelPath(p), "path %q must be accepted", p)
	}
	for _, p := range []string{"", "../evil", "a/../../evil", "/etc/passwd", `a\b`, "a//b", ".", "..", "a/./b", "a/../b"} {
		require.Error(t, validateRelPath(p), "path %q must be rejected", p)
	}
}
