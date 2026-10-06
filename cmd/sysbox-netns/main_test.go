package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNsenterCommandArgs(t *testing.T) {
	cmd := nsenterCommand("/proc/123/ns/net", []string{"list", "ruleset"})
	require.Equal(t, []string{"nsenter", "--net=/proc/123/ns/net", "nft", "list", "ruleset"}, cmd.Args)
	require.NotNil(t, cmd.SysProcAttr)
	require.Equal(t, syscall.SIGKILL, cmd.SysProcAttr.Pdeathsig)
}

func TestNsenterCommandNamedNetns(t *testing.T) {
	cmd := nsenterCommand("/var/run/netns/isolated0", []string{"-f", "-"})
	require.Equal(t, []string{"nsenter", "--net=/var/run/netns/isolated0", "nft", "-f", "-"}, cmd.Args)
}

func TestRunUsageError(t *testing.T) {
	var buf bytes.Buffer
	code := run(nil, strings.NewReader(""), io.Discard, &buf)
	require.Equal(t, 2, code)
	require.Contains(t, buf.String(), "usage: sysbox-netns")
}

func TestRunPassesOpenNamespaceToNsenter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "namespace")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	t.Setenv("TEST_NAMESPACE_PATH", path)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nsenter"), []byte(`#!/bin/sh
mv "$TEST_NAMESPACE_PATH" "$TEST_NAMESPACE_PATH.old"
printf replacement > "$TEST_NAMESPACE_PATH"
cat "${1#--net=}"
`), 0755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, stderr bytes.Buffer
	code := run([]string{path, "list", "ruleset"}, strings.NewReader(""), &out, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Equal(t, "original", out.String())
}
