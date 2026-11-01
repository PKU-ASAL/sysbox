package hostexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestResetReturnCannotPublishRetiredRevision(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "global-revisions")
	require.NoError(t, os.Mkdir(catalog, 0700))
	id := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(filepath.Join(catalog, id+".json.retired"), []byte("proof"), 0600))
	b := NewLocalBridge(LocalOptions{Topology: "lab", RunsDir: root})
	path := b.CheckpointFile("lab", "run")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	raw, err := json.Marshal(runtime.OperationCheckpoint{RunID: "run", Topology: "lab", Operation: "reset", EndedAt: time.Now()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	require.Error(t, b.RecordResetReturn(&controlplane.Run{ID: "run", Topology: "lab", Revision: id}))
	require.NoFileExists(t, path+".returned")
}
