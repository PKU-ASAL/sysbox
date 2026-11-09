package libvirt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// The fake virsh records the exact request envelope; no VM or guest is used.
func recordQGARequests(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	log := filepath.Join(root, "requests.jsonl")
	t.Setenv("SYSBOX_QGA_TEST_REQUESTS", log)
	require.NoError(t, os.WriteFile(filepath.Join(root, "virsh"), []byte(`#!/usr/bin/env python3
import base64,json,os,sys
raw=sys.argv[-1]
request=json.loads(raw)
with open(os.environ['SYSBOX_QGA_TEST_REQUESTS'],'a') as output:
    output.write(raw+'\n')
command=request['execute']
if command=='guest-file-open': result=17
elif command=='guest-file-write': result={'count':len(base64.b64decode(request['arguments']['buf-b64']))}
elif command=='guest-exec': result={'pid':42}
else: result={}
print(json.dumps({'return':result}))
`), 0700))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestQGAFileCopyKeepsEveryEncodedChunkInsideEnvelope(t *testing.T) {
	log := recordQGARequests(t)
	source := filepath.Join(t.TempDir(), "data.bin")
	data := make([]byte, 100*1024+7)
	for i := range data {
		data[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(source, data, 0600))
	require.NoError(t, (&qgaConnection{domain: "test-domain"}).CopyFile(context.Background(), source, `C:\test.bin`))
	file, err := os.Open(log)
	require.NoError(t, err)
	defer file.Close()
	dec := json.NewDecoder(file)
	var received []byte
	writes := 0
	for dec.More() {
		var raw json.RawMessage
		require.NoError(t, dec.Decode(&raw))
		require.LessOrEqual(t, len(raw), 64<<10)
		var req struct {
			Execute   string `json:"execute"`
			Arguments struct {
				Data string `json:"buf-b64"`
			} `json:"arguments"`
		}
		require.NoError(t, json.Unmarshal(raw, &req))
		if req.Execute == "guest-file-write" {
			chunk, err := base64.StdEncoding.DecodeString(req.Arguments.Data)
			require.NoError(t, err)
			received = append(received, chunk...)
			writes++
		}
	}
	require.Greater(t, writes, 1)
	require.Equal(t, data, received)
}

func TestQGAEmptyEnvironmentIsNotAnEnvironmentReplacement(t *testing.T) {
	log := recordQGARequests(t)
	c := &qgaConnection{domain: "test-domain"}
	_, err := c.start(context.Background(), substrate.ExecRequest{Program: "cmd.exe", Shell: substrate.ShellNone}, false)
	require.NoError(t, err)
	raw, err := os.ReadFile(log)
	require.NoError(t, err)
	var req struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(raw, &req))
	require.NotContains(t, req.Arguments, "env")
}
