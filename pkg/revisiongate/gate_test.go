package revisiongate

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceDecodeHandlesEscapesAndArbitraryFiles(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	raw := []byte(`{"revision":"\u0073ha256:` + strings.Repeat("a", 64) + `"}`)
	refs, err := References(raw)
	require.NoError(t, err)
	require.True(t, refs[id])
	data, _ := json.Marshal(map[string][]byte{"unit": []byte("[Unit]\nDescription=test")})
	_, err = References(data)
	require.NoError(t, err)
}
func TestNestedCatalogCannotShadowRetirement(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "global-revisions")
	require.NoError(t, os.Mkdir(catalog, 0700))
	id := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(filepath.Join(catalog, id+".json.retired"), []byte("proof"), 0600))
	nested := filepath.Join(root, "lab")
	require.NoError(t, os.MkdirAll(filepath.Join(nested, "global-revisions"), 0700))
	_, err := Guard(context.Background(), filepath.Join(nested, "runs.jsonl"), []byte(`{"revision":"`+id+`"}`))
	require.Error(t, err)
}

func TestReferenceDecodeIncludesEscapedMapKeys(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	refs, err := References([]byte(`{"\u0073ha256:` + strings.Repeat("b", 64) + `":true}`))
	require.NoError(t, err)
	require.True(t, refs[id])
}
