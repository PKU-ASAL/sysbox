package main

import (
	"bytes"
	"io"
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
