package effectgate

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerCannotPublishRetiredRevision(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "global-revisions")
	require.NoError(t, os.Mkdir(catalog, 0700))
	id := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(filepath.Join(catalog, id+".json.retired"), []byte("proof"), 0600))
	path := filepath.Join(root, "ledger.json")
	require.Error(t, save(path, &ledger{Outcomes: map[string]outcome{"admission": {Admission: json.RawMessage(`{"revision":"` + id + `"}`)}}}))
	require.NoFileExists(t, path)
}
