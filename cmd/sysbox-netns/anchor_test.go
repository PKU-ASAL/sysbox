package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnchorNameRejectsPathsAndOptions(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../other", "a/b", "-all", "a b", "a\n", "a\\b"} {
		require.Error(t, validateAnchorName(name), name)
	}
	require.NoError(t, validateAnchorName("sysbox-net-lab-test"))
}

func TestAnchorIdentityRejectsOrdinaryFileAndSymlink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "namespace")
	require.NoError(t, os.WriteFile(p, nil, 0600))
	_, _, err := anchorIdentity(p)
	require.Error(t, err)
	require.NoError(t, os.Symlink("/proc/self/ns/net", p+"-link"))
	_, _, err = anchorIdentity(p + "-link")
	require.Error(t, err)
}

func TestAnchorRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary user")
	}
	var stderr bytes.Buffer
	require.Equal(t, 1, runAnchor([]string{"--anchor-create", "test"}, io.Discard, &stderr))
	require.Contains(t, stderr.String(), "requires root")
}

func TestAnchorPinsExecutingInodeAcrossHostMountEntry(t *testing.T) {
	self, err := os.Open("/proc/self/exe")
	require.NoError(t, err)
	defer self.Close()
	cmd := anchorHostCommand(context.Background(), self, []string{"--anchor-create", "sysbox-net-test"})
	require.Equal(t, []string{"nsenter", "--no-fork", "--mount=/proc/1/ns/mnt", "--net=/proc/1/ns/net", "--", "/proc/self/fd/3", "--anchor-local", "--anchor-create", "sysbox-net-test"}, cmd.Args)
	require.Equal(t, []*os.File{self}, cmd.ExtraFiles)
	require.NotZero(t, cmd.SysProcAttr.Pdeathsig)
}

func TestAnchorDescriptorSurvivesNsenterExecBoundary(t *testing.T) {
	// A subprocess must actually receive FD 3, not merely advertise its name.
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "nsenter")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nset -eu\n[ \"$1\" = --no-fork ]\nshift 4\n[ \"$1\" = /proc/self/fd/3 ]\n[ -r \"$1\" ]\nreadlink \"$1\"\n"), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	self, err := os.Open("/proc/self/exe")
	require.NoError(t, err)
	defer self.Close()
	output, err := anchorHostCommand(context.Background(), self, []string{"--anchor-create", "sysbox-net-test"}).CombinedOutput()
	require.NoError(t, err, string(output))
	target, err := os.Readlink("/proc/self/exe")
	require.NoError(t, err)
	require.Equal(t, target, strings.TrimSpace(string(output)))
}
