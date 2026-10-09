package guesttask

import (
	"bytes"
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFileInspectionUnknownHandleDoesNotCreateJournal(t *testing.T) {
	e, _, _, _, _ := installFixture(t)
	_, err := e.InspectInstalledFile(context.Background(), substrate.FileInstallHandle{ID: "never-installed"})
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(e.Root, ".installs"))
	require.True(t, os.IsNotExist(err), "unknown inspection must not create journal/lock files")
}

func TestInstalledFileInspectionDistinguishesHistoryAndCurrentState(t *testing.T) {
	for _, scenario := range []string{"present", "content", "mode", "replacement", "missing", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			e, req, grant, _, body := installFixture(t)
			ctx := context.Background()
			record, err := e.InstallFile(ctx, req, grant, bytes.NewReader(body))
			require.NoError(t, err)
			switch scenario {
			case "content":
				require.NoError(t, os.WriteFile(req.Path, []byte("changed"), 0640))
			case "mode":
				require.NoError(t, os.Chmod(req.Path, 0600))
			case "replacement":
				require.NoError(t, os.Remove(req.Path))
				require.NoError(t, os.WriteFile(req.Path, body, 0640))
			case "missing":
				require.NoError(t, os.Remove(req.Path))
			case "symlink":
				require.NoError(t, os.Remove(req.Path))
				require.NoError(t, os.Symlink("/etc/passwd", req.Path))
			}
			inspection, err := e.InspectInstalledFile(ctx, record.Handle)
			if scenario == "symlink" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want := "drifted"
			if scenario == "present" {
				want = "present"
			}
			if scenario == "missing" {
				want = "absent"
			}
			require.Equal(t, want, inspection.Status)
			history, err := e.ObserveFileInstall(ctx, record.Handle)
			require.NoError(t, err)
			require.Equal(t, "committed", history.Result.Outcome)
		})
	}
}
