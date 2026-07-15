package commands

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/buildinfo"
)

func TestVersionCommandTextOutput(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "sysbox dev\n")
	require.Contains(t, out.String(), "commit: unknown\n")
	require.Contains(t, out.String(), "build time: unknown\n")
	require.Contains(t, out.String(), "go: go")
}

func TestVersionCommandJSONOutput(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	require.NoError(t, cmd.Execute())
	var got buildinfo.Info
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, buildinfo.Current(), got)
}
