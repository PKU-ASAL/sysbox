package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/stretchr/testify/require"
)

func TestManagedInventoryReturnsVerifiedManifestIdentity(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	items, err := s.ListManagedNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, h.ID, items[0].ID)
	require.Equal(t, h.Generation, items[0].Generation)
	hs, ok := items[0].Provider.(*HandleState)
	require.True(t, ok)
	require.Equal(t, h.Provider.(*HandleState).ManifestPath, hs.ManifestPath)
}

func TestManifestLookupDoesNotInterpretCorruptionAsAbsence(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	require.NoError(t, os.WriteFile(rawManifestPath(h), []byte(`{`), 0600))
	_, _, err := s.findAdoptionManifest(context.Background(), h.ID)
	require.Error(t, err)
	require.False(t, driver.IsCategory(err, driver.ErrorNotFound), "corrupt evidence cannot authorize absence-based cleanup")
}

func TestManagedInventoryRejectsCorruptAndDuplicateRecords(t *testing.T) {
	for _, scenario := range []string{"corrupt", "duplicate", "config", "symlink", "manifest-directory", "root-file"} {
		t.Run(scenario, func(t *testing.T) {
			s, h, raw, _ := manifestFixture(t, false)
			path := rawManifestPath(h)
			switch scenario {
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte(`{`), 0600))
			case "duplicate":
				dir := filepath.Join(s.rootfsDir, "duplicate")
				require.NoError(t, os.MkdirAll(dir, 0700))
				writeFixtureManifest(t, dir, raw)
			case "config":
				require.NoError(t, os.WriteFile(h.Provider.(*HandleState).ConfigPath, []byte(`{"changed":true}`), 0600))
			case "symlink":
				require.NoError(t, os.Symlink(filepath.Dir(path), filepath.Join(s.rootfsDir, "linked")))
			case "manifest-directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0700))
			case "root-file":
				s.rootfsDir = filepath.Join(t.TempDir(), "not-a-directory")
				require.NoError(t, os.WriteFile(s.rootfsDir, []byte("invalid root"), 0600))
			}
			items, err := s.ListManagedNodes(context.Background())
			require.Error(t, err)
			require.Nil(t, items, "partial inventory must not escape on ambiguity")
		})
	}
}
